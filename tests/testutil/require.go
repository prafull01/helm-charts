package testutil

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach-operator/pkg/database"
	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/retry"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	TestDBName = "test_db"
)

type CockroachCluster struct {
	Cfg                        *rest.Config
	K8sClient                  client.Client
	StatefulSetName, Namespace string
	ClientSecret, NodeSecret   string
	CaSecret                   string
	IsCaUserProvided           bool
	DesiredNodes               int
	Context                    string
}

// RequireClusterToBeReadyEventuallyTimeout waits for all the CRDB pods to come into running state.
func RequireClusterToBeReadyEventuallyTimeout(t *testing.T, crdbCluster CockroachCluster, timeout time.Duration) {
	err := wait.Poll(10*time.Second, timeout, func() (bool, error) {
		ss, err := fetchStatefulSet(crdbCluster.K8sClient, crdbCluster.StatefulSetName, crdbCluster.Namespace)
		if err != nil {
			t.Logf("error fetching stateful set")
			return false, err
		}

		if ss == nil {
			t.Logf("stateful set is not found")
			return false, nil
		}

		if !statefulSetIsReady(ss) {
			t.Logf("stateful set is not ready")
			logPods(context.TODO(), ss, crdbCluster.Cfg, t)
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err)
}

// RequireCRDBClusterToBeReadyEventuallyTimeout waits for all the CockroachDB pods to come into running state.
func RequireCRDBClusterToBeReadyEventuallyTimeout(t *testing.T, opts *k8s.KubectlOptions, crdbCluster CockroachCluster, timeout time.Duration) {
	err := wait.Poll(10*time.Second, timeout, func() (bool, error) {
		pods, err := k8s.ListPodsE(t, opts, metav1.ListOptions{
			LabelSelector: "app=cockroachdb",
		})
		if len(pods) != crdbCluster.DesiredNodes {
			t.Logf("expected %d crdb pods; found %d", crdbCluster.DesiredNodes, len(pods))
			return false, nil
		}
		for _, pod := range pods {
			if !k8s.IsPodAvailable(&pod) {
				t.Logf("pod %s not ready", pod.Name)
				return false, nil
			}
		}
		if err != nil {
			return false, err
		}
		require.True(t, len(pods) == crdbCluster.DesiredNodes)
		return true, nil
	})
	require.NoError(t, err)
}

func RequirePodToBeCreatedAndReady(t *testing.T, opts *k8s.KubectlOptions, podName string, timeout time.Duration) {
	require.NoError(t, wait.Poll(10*time.Second, timeout, func() (done bool, err error) {
		pod, err := k8s.GetPodE(t, opts, podName)
		if err != nil {
			return false, nil
		}
		if !k8s.IsPodAvailable(pod) {
			t.Logf("pod %s not ready", pod.Name)
			return false, nil
		}
		return true, nil
	}))
}

func logPods(ctx context.Context, sts *appsv1.StatefulSet, cfg *rest.Config, t *testing.T) {
	// create a new clientset to talk to k8s.
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Log("could not able to create kubernetes clientset, will not able to print logs")
	}

	// Fetching all the pods in a namespace.
	options := metav1.ListOptions{}

	// Get all pods.
	podList, err := clientset.CoreV1().Pods(sts.Namespace).List(ctx, options)
	if err != nil {
		t.Log("could not able to get the pods, will not able to print logs")
	}

	if len(podList.Items) == 0 {
		t.Log("no pods found")
	}

	// Print out pretty into on the Pods
	for _, podInfo := range (*podList).Items {
		t.Logf("pods-name=%v\n", podInfo.Name)
		t.Logf("pods-status=%v\n", podInfo.Status.Phase)
		t.Logf("pods-condition=%v\n", podInfo.Status.Conditions)
	}
}

func fetchStatefulSet(k8sClient client.Client, name, namespace string) (*appsv1.StatefulSet, error) {
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	if err := k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, ss); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}

		return nil, err
	}

	return ss, nil
}

func statefulSetIsReady(ss *appsv1.StatefulSet) bool {
	return ss.Status.ReadyReplicas == ss.Status.Replicas
}

func getDBConn(t *testing.T, crdbCluster CockroachCluster, dbName string, podName string) *sql.DB {
	isSecure := crdbCluster.CaSecret != ""
	sqlPort := int32(26257)

	serviceName := podName
	if serviceName == "" {
		serviceName = fmt.Sprintf("%s-0.%s", crdbCluster.StatefulSetName, crdbCluster.StatefulSetName)
	}
	conn := &database.DBConnection{
		Ctx:    context.TODO(),
		Client: crdbCluster.K8sClient,
		Port:   &sqlPort,
		UseSSL: isSecure,

		RestConfig:   crdbCluster.Cfg,
		ServiceName:  serviceName,
		Namespace:    crdbCluster.Namespace,
		DatabaseName: dbName,

		RunningInsideK8s:            false,
		ClientCertificateSecretName: crdbCluster.ClientSecret,
		RootCertificateSecretName:   crdbCluster.NodeSecret,
	}

	// Create a new database connection for the update.
	db, err := database.NewDbConnection(conn)
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Close()
	})
	return db
}

// RequireCRDBDatabaseToFunction creates a database, a table and insert two rows.
func RequireCRDBDatabaseToFunction(t *testing.T, crdbCluster CockroachCluster, dbName string, podName string) {
	systemDB := getDBConn(t, crdbCluster, "system", podName)
	if _, err := systemDB.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", dbName)); err != nil {
		t.Fatal(err)
	}

	db := getDBConn(t, crdbCluster, dbName, podName)
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS accounts (id INT PRIMARY KEY, balance INT)"); err != nil {
		t.Fatal(err)
	}

	// Insert two rows into the "accounts" table,
	// This won't error out if the records are already present,
	// as in some tests we verify CockroachDB cluster multiple times.
	if _, err := db.Exec(
		"INSERT INTO accounts (id, balance) VALUES (1, 1000), (2, 250) ON CONFLICT DO NOTHING"); err != nil {
		t.Fatal(err)
	}
}

// RequireCRDBToFunction creates a database, a table and insert two rows if it is a fresh installation of the cluster.
// If validateExistingData is true, this will check that existing two rows are present.
func RequireCRDBToFunction(t *testing.T, crdbCluster CockroachCluster, validateExistingData bool) {
	db := getDBConn(t, crdbCluster, "system", "")

	if validateExistingData {
		t.Log("Verifying the existing data in the database after certificate rotation")
	}

	// Create database only if we are testing crdb install
	if !validateExistingData {
		if _, err := db.Exec("CREATE DATABASE IF NOT EXISTS test_db"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.Exec("USE test_db"); err != nil {
		t.Fatal(err)
	}

	// Create and insert into table only for the crdb install
	if !validateExistingData {
		// Create the "accounts" table.
		if _, err := db.Exec("CREATE TABLE IF NOT EXISTS accounts (id INT PRIMARY KEY, balance INT)"); err != nil {
			t.Fatal(err)
		}

		// empty table if exists as we can use the RequireCRDBToFunction multiple times in same test.
		if _, err := db.Exec("DELETE FROM accounts"); err != nil {
			t.Fatal(err)
		}

		// Insert two rows into the "accounts" table.
		if _, err := db.Exec(
			"INSERT INTO accounts (id, balance) VALUES (1, 1000), (2, 250)"); err != nil {
			t.Fatal(err)
		}
	}

	// Print out the balances.
	rows, err := db.Query("SELECT id, balance FROM accounts")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	t.Log("Initial balances:")
	for rows.Next() {
		var id, balance int
		if err := rows.Scan(&id, &balance); err != nil {
			t.Fatal(err)
		}
		t.Log("balances", id, balance)
	}

	countRows, err := db.Query("SELECT COUNT(*) as count FROM accounts")
	if err != nil {
		t.Fatal(err)
	}
	defer countRows.Close()
	count := getCount(t, countRows)
	if count != 2 {
		t.Fatal(fmt.Errorf("found incorrect number of rows.  Expected 2 got %v", count))
	}

	t.Log("finished testing database")
}

func RequireCRDBClusterToFunction(t *testing.T, crdbCluster CockroachCluster, rotate bool, podName string) {
	db := getDBConn(t, crdbCluster, "system", podName)

	if rotate {
		t.Log("Verifying the existing data in the database after certificate rotation")
	}

	// Create database only if we are testing crdb install
	if !rotate {
		if _, err := db.Exec("CREATE DATABASE IF NOT EXISTS test_db"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.Exec("USE test_db"); err != nil {
		t.Fatal(err)
	}

	// Create and insert into table only for the crdb install
	if !rotate {
		// Create the "accounts" table.
		if _, err := db.Exec("CREATE TABLE IF NOT EXISTS accounts (id INT PRIMARY KEY, balance INT)"); err != nil {
			t.Fatal(err)
		}

		// Insert two rows into the "accounts" table.
		if _, err := db.Exec(
			"INSERT INTO accounts (id, balance) VALUES (1, 1000), (2, 250) ON CONFLICT DO NOTHING"); err != nil {
			t.Fatal(err)
		}
	}

	// Print out the balances.
	rows, err := db.Query("SELECT id, balance FROM accounts")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	t.Log("Initial balances:")
	for rows.Next() {
		var id, balance int
		if err := rows.Scan(&id, &balance); err != nil {
			t.Fatal(err)
		}
		t.Log("balances", id, balance)
	}

	countRows, err := db.Query("SELECT COUNT(*) as count FROM accounts")
	if err != nil {
		t.Fatal(err)
	}
	defer countRows.Close()
	count := getCount(t, countRows)
	if count != 2 {
		t.Fatal(fmt.Errorf("found incorrect number of rows.  Expected 2 got %v", count))
	}

	t.Log("finished testing database")
}

func getCount(t *testing.T, rows *sql.Rows) (count int) {
	for rows.Next() {
		err := rows.Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
	}
	return count
}

// RequireCertificatesToBeValid will check the CA certificate and client certificate validity from their respective secrets.
// Also, it verifies that node certificates are signed by the CA certificates used in the cluster.
func RequireCertificatesToBeValid(t *testing.T, crdbCluster CockroachCluster) {
	t.Log("Verifying the Certificates")

	kubeConfig, err := k8s.GetKubeConfigPathE(t)
	require.NoError(t, err)
	kubectlOptions := k8s.NewKubectlOptions(crdbCluster.Context, kubeConfig, crdbCluster.Namespace)

	// Get the node certificate secret and load the node cert
	nodeSecret := k8s.GetSecret(t, kubectlOptions, crdbCluster.NodeSecret)
	nodeCert := LoadCertificate(t, nodeSecret, "tls.crt")

	t.Log("Verifying the node certificate validity with its secret")
	require.Equal(t, nodeCert.NotBefore.Format(time.RFC3339), nodeSecret.Annotations["certificate-valid-from"])
	require.Equal(t, nodeCert.NotAfter.Format(time.RFC3339), nodeSecret.Annotations["certificate-valid-upto"])

	t.Log("Verifying node certs are signed by CA certificates")
	verifyCertificate(t, nodeSecret.Data["ca.crt"], nodeCert)

	clientSecret := k8s.GetSecret(t, kubectlOptions, crdbCluster.ClientSecret)
	clientCert := LoadCertificate(t, clientSecret, "tls.crt")

	t.Log("Verifying the client certificate validity with its secret")
	require.Equal(t, clientCert.NotBefore.Format(time.RFC3339), clientSecret.Annotations["certificate-valid-from"])
	require.Equal(t, clientCert.NotAfter.Format(time.RFC3339), clientSecret.Annotations["certificate-valid-upto"])

	// Get the CA certificate secret and load the ca cert
	var caCert *x509.Certificate
	caSecret := k8s.GetSecret(t, kubectlOptions, crdbCluster.CaSecret)
	if _, ok := caSecret.Data["ca.crt"]; !ok {
		caCert = LoadCertificate(t, nodeSecret, "ca.crt")
	} else {
		caCert = LoadCertificate(t, caSecret, "ca.crt")
	}

	if !crdbCluster.IsCaUserProvided {
		t.Log("Verifying the CA certificate validity with its secret")
		require.Equal(t, caCert.NotBefore.Format(time.RFC3339), caSecret.Annotations["certificate-valid-from"])
		require.Equal(t, caCert.NotAfter.Format(time.RFC3339), caSecret.Annotations["certificate-valid-upto"])
	}

	t.Log("Certificates validated successfully")
}

func LoadCertificate(t *testing.T, certSecret *corev1.Secret, key string) *x509.Certificate {
	block, _ := pem.Decode(certSecret.Data[key])
	if block == nil {
		t.Fatal(errors.New("error decoding the ca certificate"))
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	return cert
}

func verifyCertificate(t *testing.T, caCert []byte, cert *x509.Certificate) {
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caCert)

	options := x509.VerifyOptions{Roots: roots}

	_, err := cert.Verify(options)
	if err != nil {
		t.Fatal(err)
	}
}

// PrintDebugLogs adds the verbose logging of the cluster at the runtime.
func PrintDebugLogs(t *testing.T, options *k8s.KubectlOptions) {
	for _, args := range [][]string{
		{"get", "nodes"},
		{"get", "pvc"},
		{"describe", "pvc"},
		{"get", "pv"},
		{"describe", "pv"},
		{"get", "sts"},
		{"describe", "sts"},
		{"get", "pods"},
		{"describe", "pods"},
	} {
		out, err := k8s.RunKubectlAndGetOutputE(t, options, args...)
		require.NoError(t, err)
		t.Log(out)
	}
}

// RequireToRunRotateJob triggers the client/node or CA certificate rotation job based on next cron schedule.
func RequireToRunRotateJob(t *testing.T, crdbCluster CockroachCluster, values map[string]string,
	scheduleToTriggerRotation string, caRotate bool,
) {
	var args []string
	var jobName string
	imageName := fmt.Sprintf("gcr.io/cockroachlabs-helm-charts/cockroach-self-signer-cert:%s", values["tls.selfSigner.image.tag"])
	backoffLimit := int32(1)
	if caRotate {
		jobName = "ca-certificate-rotate"
		args = []string{
			"rotate",
			"--ca",
			fmt.Sprintf("--ca-duration=%s", values["tls.certs.selfSigner.caCertDuration"]),
			fmt.Sprintf("--ca-expiry=%s", values["tls.certs.selfSigner.caCertExpiryWindow"]),
			fmt.Sprintf("--ca-cron=\"%s\"", scheduleToTriggerRotation),
			"--readiness-wait=30s",
		}
	} else {
		jobName = "client-node-certificate-rotate"
		args = []string{
			"rotate",
			fmt.Sprintf("--ca-duration=%s", values["tls.certs.selfSigner.caCertDuration"]),
			fmt.Sprintf("--ca-expiry=%s", values["tls.certs.selfSigner.caCertExpiryWindow"]),
			"--client",
			fmt.Sprintf("--client-duration=%s", values["tls.certs.selfSigner.clientCertDuration"]),
			fmt.Sprintf("--client-expiry=%s", values["tls.certs.selfSigner.clientCertExpiryWindow"]),
			"--node",
			fmt.Sprintf("--node-duration=%s", values["tls.certs.selfSigner.nodeCertDuration"]),
			fmt.Sprintf("--node-expiry=%s", values["tls.certs.selfSigner.nodeCertExpiryWindow"]),
			fmt.Sprintf("--node-client-cron=\"%s\"", scheduleToTriggerRotation),
			"--readiness-wait=30s",
		}
	}
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: crdbCluster.Namespace,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{},
				Spec: corev1.PodSpec{
					RestartPolicy:      "Never",
					ServiceAccountName: fmt.Sprintf("%s-rotate-self-signer", crdbCluster.StatefulSetName),
					Containers: []corev1.Container{{
						Name:       "cert-rotate-job",
						Image:      imageName,
						Args:       args,
						WorkingDir: "",
						Ports:      nil,
						EnvFrom:    nil,
						Env: []corev1.EnvVar{
							{
								Name:  "STATEFULSET_NAME",
								Value: crdbCluster.StatefulSetName,
							},
							{
								Name:  "NAMESPACE",
								Value: crdbCluster.Namespace,
							},
							{
								Name:  "CLUSTER_DOMAIN",
								Value: "cluster.local",
							},
						},
					}},
				},
			},
			TTLSecondsAfterFinished: nil,
		},
	}

	if err := crdbCluster.K8sClient.Create(context.TODO(), job); err != nil {
		t.Fatal(err)
	}
}

// RequireCertRotateJobToBeCompleted waits for the certificate rotation job to complete.
func RequireCertRotateJobToBeCompleted(t *testing.T, jobName string, crdbCluster CockroachCluster, timeout time.Duration) {
	err := wait.Poll(10*time.Second, timeout, func() (bool, error) {
		job, err := fetchJob(crdbCluster.K8sClient, jobName, crdbCluster.Namespace)
		if err != nil {
			t.Logf("error fetching job")
			return false, err
		}

		if job == nil {
			t.Logf("job is not found")
			return false, nil
		}

		if job.Status.Active > 0 {
			t.Log("Waiting for certificate rotation job to complete")
		}

		if job.Status.Succeeded > 0 {
			return true, nil
		}

		return false, nil
	})
	require.NoError(t, err)
}

func fetchJob(k8sClient client.Client, name, namespace string) (*batchv1.Job, error) {
	job := batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}

	if err := k8sClient.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, &job); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	return &job, nil
}

// WaitUntilPodDeleted waits until the pod is deleted, retrying the check for the specified
// amount of times, sleeping for the provided duration between each try.
func WaitUntilPodDeleted(
	t *testing.T,
	options *k8s.KubectlOptions,
	podName string,
	retries int,
	sleepBetweenRetries time.Duration,
) {
	statusMsg := fmt.Sprintf("Wait for pod %s to be deleted.", podName)
	message, err := retry.DoWithRetryE(
		t,
		statusMsg,
		retries,
		sleepBetweenRetries,
		func() (string, error) {
			_, err := k8s.GetPodE(t, options, podName)
			if err != nil && kube.IsNotFound(err) {
				return "Pod is now deleted", nil
			}
			return "", fmt.Errorf("pod is not deleted: %s", err)
		},
	)
	if err != nil {
		log.Printf("Timedout waiting for Pod to be deleted: %s\n", err)
	}
	log.Println(message)
}

func PatchHelmValues(inputValues map[string]string) map[string]string {
	overrides := map[string]string{
		// Override the persistent storage size to 1Gi so that we do not run out of space.
		"storage.persistentVolume.size": "1Gi",
		// Override the terminationGracePeriodSeconds from 300s to 30 as it makes pod delete take longer.
		"statefulset.terminationGracePeriodSeconds": "30",
		// Add the app label to the statefulset.
		"statefulset.labels.app": "cockroachdb",
	}

	for k, v := range overrides {
		inputValues[k] = v
	}

	return inputValues
}

func GetGitRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		panic(fmt.Errorf("failed to find git root: %w", err))
	}
	return strings.TrimSpace(string(out))
}
