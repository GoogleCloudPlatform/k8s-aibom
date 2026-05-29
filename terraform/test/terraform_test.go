package test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/gruntwork-io/terratest/modules/retry"
	"github.com/gruntwork-io/terratest/modules/terraform"
	test_structure "github.com/gruntwork-io/terratest/modules/test-structure"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTerraformK8sAibom(t *testing.T) {
	t.Parallel()

	// 1. Load configuration from environment variables
	projectID := os.Getenv("GCP_PROJECT_ID")
	require.NotEmpty(t, projectID, "GCP_PROJECT_ID environment variable is not set")

	clusterName := os.Getenv("GKE_CLUSTER_NAME")
	require.NotEmpty(t, clusterName, "GKE_CLUSTER_NAME environment variable is not set")

	clusterLocation := os.Getenv("GKE_CLUSTER_LOCATION")
	require.NotEmpty(t, clusterLocation, "GKE_CLUSTER_LOCATION environment variable is not set")

	// 2. Generate unique identifiers to prevent collisions during concurrent CI runs
	uniqueID := strings.ToLower(random.UniqueId())
	repositoryID := fmt.Sprintf("aibom-repo-%s", uniqueID)
	namespace := fmt.Sprintf("aibom-ns-%s", uniqueID)
	imageTag := fmt.Sprintf("v1.0.0-%s", uniqueID)

	// Copy the terraform folder to a temp folder so tests can run in parallel without state collisions
	tempTestFolder := test_structure.CopyTerraformFolderToTemp(t, "../..", "terraform")

	// 3. Configure Terraform Options
	terraformOptions := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: tempTestFolder,
		Vars: map[string]interface{}{
			"project_id":       projectID,
			"cluster_name":     clusterName,
			"cluster_location": clusterLocation,
			"repository_id":    repositoryID,
			"namespace":        namespace,
			"image_tag":        imageTag,
		},
	})

	kubectlOptions := k8s.NewKubectlOptions("", "", namespace)

	// 4. Ensure we clean up resources automatically at the end of the test
	defer func() {
		terraform.Destroy(t, terraformOptions)
		// Terraform Helm provider with create_namespace=true does not delete the namespace on destroy.
		// We use RunKubectlE because the namespace might not have been created if apply failed early.
		_ = k8s.RunKubectlE(t, kubectlOptions, "delete", "namespace", namespace, "--ignore-not-found=true")
	}()

	// 5. Run `terraform init` and `terraform apply`
	terraform.InitAndApply(t, terraformOptions)

	// 6. Validation: Verify Helm deployment succeeded by inspecting the Kubernetes cluster
	// Wait for the Helm release to deploy the pods
	k8s.WaitUntilNumPodsCreated(t, kubectlOptions, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=k8s-aibom",
	}, 1, 30, 10*time.Second)

	// Poll until the pod is actually in the Running state
	retry.DoWithRetry(t, "Wait for pod to be running", 30, 10*time.Second, func() (string, error) {
		pods := k8s.ListPods(t, kubectlOptions, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=k8s-aibom",
		})
		if len(pods) == 0 {
			return "", fmt.Errorf("no pods found")
		}
		if pods[0].Status.Phase != "Running" {
			return "", fmt.Errorf("pod is not running yet, current phase: %s", pods[0].Status.Phase)
		}
		return "Pod is running", nil
	})
}
