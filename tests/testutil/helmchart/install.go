package helmchart

import (
	"context"
	"fmt"
	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	"github.com/cockroachdb/helm-charts/tests/testutil"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"testing"
	"time"
)

var (
	ReleaseName  = "crdb-test"
	cfg          = ctrl.GetConfigOrDie()
	k8sClient, _ = client.New(cfg, client.Options{})
)

const (
	role       = "crdb-test-cockroachdb-node-reader"
	TestDBName = "testdb"
)

type cockroachHelmChart interface {
	installHelm(t *testing.T, isCaProvided bool)
}

type HelmInstall struct {
	// Namespace stores mapping between cluster name and namespace.
	Namespace string

	HelmOptions *helm.Options

	CrdbCluster testutil.CockroachCluster

	cockroachHelmChart
}

func (h *HelmInstall) InstallHelm(t *testing.T) {
	kubectlOptions := k8s.NewKubectlOptions("", "", h.Namespace)

	h.HelmOptions.KubectlOptions = kubectlOptions

	_, err := k8s.GetNamespaceE(t, kubectlOptions, h.Namespace)
	if err != nil && apierrors.IsNotFound(err) {
		k8s.CreateNamespace(t, kubectlOptions, h.Namespace)
	}

	// Deploy the cockroachdb helm chart and checks installation should succeed.
	helmChartPath, _, err := HelmChartPaths()
	require.NoError(t, err)

	helm.Install(t, h.HelmOptions, helmChartPath, ReleaseName)

	// Print the debug logs in case of test failure.
	defer func() {
		if t.Failed() {
			testutil.PrintDebugLogs(t, kubectlOptions)
		}
	}()

	// Next we wait for the service endpoint.
	serviceName := fmt.Sprintf("%s-cockroachdb-public", ReleaseName)
	k8s.WaitUntilServiceAvailable(t, kubectlOptions, serviceName, 30, 2*time.Second)
}

func (h *HelmInstall) ValidateCRDB(t *testing.T) {
	tlsEnabled := h.HelmOptions.SetValues["tls.enabled"]
	selfSignerEnabled := h.HelmOptions.SetValues["tls.certs.selfSigner.enabled"]
	if (tlsEnabled == "" || tlsEnabled == "true") && (selfSignerEnabled == "" || selfSignerEnabled == "true") {
		// Verify certificates only if they are created by the self-signer utility
		testutil.RequireCertificatesToBeValid(t, h.CrdbCluster)
	}
	testutil.RequireClusterToBeReadyEventuallyTimeout(t, h.CrdbCluster, 600*time.Second)
	time.Sleep(20 * time.Second)
	testutil.RequireCRDBToFunction(t, h.CrdbCluster, false)
	testutil.RequireCRDBDatabaseToFunction(t, h.CrdbCluster, TestDBName, "")
}

func (h *HelmInstall) Uninstall(t *testing.T) {
	kubectlOptions := k8s.NewKubectlOptions("", "", h.Namespace)
	danglingSecret := []string{}
	tlsEnabled := h.HelmOptions.SetValues["tls.enabled"]
	selfSignerEnabled := h.HelmOptions.SetValues["tls.certs.selfSigner.enabled"]
	if (tlsEnabled == "" || tlsEnabled == "true") && (selfSignerEnabled == "" || selfSignerEnabled == "true") {
		// Verify cleanup of secret only if they are created by self-signer utility.
		danglingSecret = append(danglingSecret, h.CrdbCluster.ClientSecret)
		danglingSecret = append(danglingSecret, h.CrdbCluster.NodeSecret)
		if !h.CrdbCluster.IsCaUserProvided && h.CrdbCluster.CaSecret != "" {
			danglingSecret = append(danglingSecret, h.CrdbCluster.CaSecret)
		}
	}

	t.Log(danglingSecret)
	cleanupResources(
		t,
		ReleaseName,
		kubectlOptions,
		h.HelmOptions,
		danglingSecret,
	)
	if h.CrdbCluster.IsCaUserProvided {
		// custom user CA certificate secret should not be deleted by pre-delete job.
		_, err := k8s.GetSecretE(t, kubectlOptions, h.CrdbCluster.CaSecret)
		require.NoError(t, err)
	}
	k8s.DeleteNamespace(t, kubectlOptions, h.Namespace)
}

func cleanupResources(
	t *testing.T,
	releaseName string,
	kubectlOptions *k8s.KubectlOptions,
	options *helm.Options,
	danglingSecrets []string,
) {
	err := helm.DeleteE(t, options, releaseName, true)
	// Ignore the error if the operation timed out.
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		require.NoError(t, err)
	} else {
		t.Logf("Error while deleting helm release: %v", err)
	}

	for i := range danglingSecrets {
		_, err = k8s.GetSecretE(t, kubectlOptions, danglingSecrets[i])
		require.Equal(t, true, kube.IsNotFound(err))
		t.Logf("Secret %s deleted by helm uninstall", danglingSecrets[i])
	}

	crb := &rbacv1.ClusterRoleBinding{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: role}, crb); err != nil {
		t.Logf("Error getting ClusterRoleBinding %s: %v", role, err)
	}

	if err := k8sClient.Delete(context.Background(), crb); err != nil {
		t.Logf("Error deleting ClusterRoleBinding %s: %v", role, err)
	}
	cr := &rbacv1.ClusterRole{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Name: role}, cr); err != nil {
		t.Logf("Error getting ClusterRole %s: %v", role, err)
	}

	if err := k8sClient.Delete(context.Background(), cr); err != nil {
		t.Logf("Error deleting ClusterRole %s: %v", role, err)
	}
}

func HelmChartPaths() (string, string, error) {
	helmChartPath, err := filepath.Abs("../../../cockroachdb")
	if err != nil {
		return "", "", err
	}

	operatorChartPath, err := filepath.Abs("../../../operator")
	if err != nil {
		return "", "", err
	}

	return helmChartPath, operatorChartPath, nil
}
