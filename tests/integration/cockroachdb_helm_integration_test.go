package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
)

// This file contains examples of how to use terratest to test helm charts by deploying the chart and verifying the
// deployment by hitting the service endpoint.
func TestHelmBasicExampleDeployment(t *testing.T) {
	t.Parallel()

	// Path to the helm chart we will test
	helmChartPath, err := filepath.Abs("../../cockroachdb")
	require.NoError(t, err)

	// To ensure we can reuse the resource config on the same cluster to test different scenarios, we setup a unique
	// namespace for the resources for this test.
	// Note that namespaces must be lowercase.
	namespaceName := fmt.Sprintf("helm-basic-example-%s", strings.ToLower(random.UniqueId()))

	// Setup the kubectl config and context. Here we choose to use the defaults, which is:
	// - HOME/.kube/config for the kubectl config file
	// - Current context of the kubectl config file
	kubectlOptions := k8s.NewKubectlOptions("", "", namespaceName)

	k8s.CreateNamespace(t, kubectlOptions, namespaceName)
	// ... and make sure to delete the namespace at the end of the test
	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)

	// Setup the args. For this test, we will set the following input values:
	options := &helm.Options{
		KubectlOptions: kubectlOptions,
	}

	// We generate a unique release name so that we can refer to after deployment.
	// By doing so, we can schedule the delete call here so that at the end of the test, we run
	// `helm delete RELEASE_NAME` to clean up any resources that were created.
	releaseName := fmt.Sprintf(
		"crdb-%s",
		strings.ToLower(random.UniqueId()),
	)
	defer helm.Delete(t, options, releaseName, true)

	// Deploy the chart using `helm install`. Note that we use the version without `E`, since we want to assert the
	// install succeeds without any errors.
	helm.Install(t, options, helmChartPath, releaseName)
}
