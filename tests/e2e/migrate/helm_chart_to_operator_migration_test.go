package migrate

import (
	"context"
	"fmt"
	"github.com/cockroachdb/helm-charts/tests/e2e/operator"
	"github.com/cockroachdb/helm-charts/tests/testutil"
	"github.com/cockroachdb/helm-charts/tests/testutil/helmchart"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/gruntwork-io/terratest/modules/shell"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	cfg                 = ctrl.GetConfigOrDie()
	k8sClient, _        = client.New(cfg, client.Options{})
	releaseName         = "crdb-test"
	k3dClusterName      = "k3d-chart-testing-cluster"
	stsName             = fmt.Sprintf("%s-cockroachdb", releaseName)
	ClientSecret        = fmt.Sprintf("%s-cockroachdb-client-secret", releaseName)
	NodeSecret          = fmt.Sprintf("%s-cockroachdb-node-secret", releaseName)
	CASecret            = fmt.Sprintf("%s-cockroachdb-ca-secret", releaseName)
	rootPath            = testutil.GetGitRoot()
	migrationScriptPath = filepath.Join(rootPath, "scripts", "migration")
	manifestsDirPath    = filepath.Join(rootPath, "manifests")
)

type HelmChartToOperator struct {
	helmchart.HelmInstall
}

func newHelmChartToOperator() *HelmChartToOperator {
	return &HelmChartToOperator{}
}

func TestHelmChartToOperatorMigration(t *testing.T) {
	h := newHelmChartToOperator()
	t.Run("", h.TestDefaultMigration)
}

func (h *HelmChartToOperator) TestDefaultMigration(t *testing.T) {
	isCaUserProvided := false
	h.Namespace = "cockroach" + strings.ToLower(random.UniqueId())
	h.CrdbCluster = testutil.CockroachCluster{
		Cfg:              cfg,
		K8sClient:        k8sClient,
		StatefulSetName:  fmt.Sprintf("%s-cockroachdb", releaseName),
		Namespace:        h.Namespace,
		ClientSecret:     ClientSecret,
		NodeSecret:       NodeSecret,
		CaSecret:         CASecret,
		IsCaUserProvided: isCaUserProvided,
		//Context:          k3dClusterName,
	}
	h.HelmOptions = &helm.Options{
		SetValues: testutil.PatchHelmValues(map[string]string{
			"operator.enabled":                         "false",
			"conf.cluster-name":                        "test",
			"init.provisioning.enabled":                "true",
			"init.provisioning.databases[0].name":      helmchart.TestDBName,
			"init.provisioning.databases[0].owners[0]": "root",
		}),
	}

	kubectlOptions := k8s.NewKubectlOptions("", "", h.Namespace)

	h.InstallHelm(t)
	//defer h.Uninstall(t)
	h.ValidateCRDB(t)

	t.Log("Migrate the existing helm chart to Cockroach Enterprise Operator")

	t.Log("Updating the existing certs")
	// generate-certs.sh script uses releaseName to identify the certificate secret
	os.Setenv("RELEASE_NAME", releaseName)
	os.Setenv("NAMESPACE", h.Namespace)
	certGenration := shell.Command{
		Command: filepath.Join(migrationScriptPath, "helm", "generate-certs.sh"),
	}
	t.Log(shell.RunCommandAndGetOutput(t, certGenration))

	require.NoError(t, os.Mkdir(manifestsDirPath, 0700))
	defer func() {
		_ = os.RemoveAll(manifestsDirPath)
	}()

	t.Log("Generate manifests to migrate")
	generateManifestsCmd := shell.Command{
		Command: filepath.Join(rootPath, "bin", "migration-helper"),
		Args: []string{
			"build-manifest",
			"helm",
			fmt.Sprintf("--statefulset-name=%s", stsName),
			fmt.Sprintf("--namespace=%s", h.Namespace),
			"--cloud-provider=k3d",
			"--cloud-region=us-east1",
			fmt.Sprintf("--output-dir=%s", manifestsDirPath),
		},
	}
	t.Log(shell.RunCommandAndGetOutput(t, generateManifestsCmd))

	t.Log("Install the cockroachdb enterprise operator")
	k8s.RunKubectl(t, kubectlOptions, "create", "priorityclass", "crdb-critical", "--value", "500000000")
	operator.InstallCockroachDBEnterpriseOperator(t, kubectlOptions)

	var crdbSts = appsv1.StatefulSet{}
	err := k8sClient.Get(context.TODO(), types.NamespacedName{Name: stsName, Namespace: h.Namespace}, &crdbSts)
	require.NoError(t, err)

	crdbPodCount := int(*crdbSts.Spec.Replicas)
	for idx := crdbPodCount - 1; idx >= 0; idx-- {
		t.Logf("Scaling statefulset %s to %d", stsName, idx)
		k8s.RunKubectl(t, kubectlOptions, "scale", "statefulset", stsName, "--replicas", strconv.Itoa(idx))

		podName := fmt.Sprintf("%s-%d", stsName, idx)
		testutil.WaitUntilPodDeleted(t, kubectlOptions, podName, 30, 2*time.Second)
		k8s.RunKubectl(t, kubectlOptions, "apply", "-f", filepath.Join(manifestsDirPath, fmt.Sprintf("crdbnode-%d.yaml", idx)))
		testutil.RequirePodToBeCreatedAndReady(t, kubectlOptions, podName, 300*time.Second)
	}

	t.Log("All the statefulset pods are migrated to CrdbNodes")
	t.Log("Update the public service")
	k8s.RunKubectl(t, kubectlOptions, "apply", "-f", filepath.Join(manifestsDirPath, "public-service.yaml"))
	k8s.RunKubectl(t, kubectlOptions, "delete", "poddisruptionbudget", fmt.Sprintf("%s-budget", stsName))

	helm.Upgrade(t, &helm.Options{
		ValuesFiles: []string{filepath.Join(manifestsDirPath, "values.yaml")},
	}, filepath.Join(testutil.GetGitRoot(), "cockroachdb"), releaseName)

	h.ValidateCRDB(t)
}
