package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/random"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/gruntwork-io/terratest/modules/k8s"
)

func TestBasicClaimFlow(t *testing.T) {
	t.Parallel()

	// create specific namespace for the test
	namespaceName := fmt.Sprintf("kubernetes-basic-example-%s", strings.ToLower(random.UniqueId()))
	options := k8s.NewKubectlOptions("", "", namespaceName)

	k8s.CreateNamespace(t, options, namespaceName)
	defer k8s.DeleteNamespace(t, options, namespaceName)

	// ensure that the provider is deployed and ready
	applyFromPath(t, "testData/provider.yaml", options)
	if err := waitForConditionStatus(t, "providers", "provider-dummy", namespaceName, installedAndHealthyConditions, ""); err != nil {
		t.Fatalf("Error waiting for provider to be installed and healthy: %v", err)
	}
	applyFromPath(t, "testData/providerconfig.yaml", options)

	applyFromPath(t, "testData/deployment.yaml", &k8s.KubectlOptions{Namespace: "crossplane-system"})
	applyFromPath(t, "testData/service.yaml", &k8s.KubectlOptions{Namespace: "crossplane-system"})

	// install the xrd
	applyFromPath(t, "testData/xrd.yaml", options)
	if err := waitForConditionStatus(t, "xrd", "xrobots.dummy.crossplane.io", namespaceName, establishedAndOfferedConditions, ""); err != nil {
		t.Fatalf("Error waiting for xrd to be installed and healthy: %v", err)
	}
	// install the composition
	applyFromPath(t, "testData/composition.yaml", options)
	// apply claim
	applyFromPath(t, "testData/claim.yaml", options)

	// check if claim is ready and synced
	if err := waitForConditionStatus(t, "claim", "test-robot", namespaceName, syncAndReadyConditions, ""); err != nil {
		t.Fatalf("Error waiting for claim to be ready and synced: %v", err)
	}

	// check if composite is ready and synced
	if err := waitForConditionStatus(t, "composite", "", namespaceName, syncAndReadyConditions, fmt.Sprintf("crossplane.io/claim-namespace=%s", namespaceName)); err != nil {
		t.Fatalf("Error waiting for composite to be ready and synced: %v", err)
	}
}

var syncAndReadyConditions = map[string]string{"Synced": "True", "Ready": "True"}
var installedAndHealthyConditions = map[string]string{"Installed": "True", "Healthy": "True"}
var establishedAndOfferedConditions = map[string]string{"Established": "True", "Offered": "True"}

func waitForConditionStatus(t *testing.T, kind, name, namespace string, conditions map[string]string, label string) error {
	err := wait.PollImmediate(5*time.Second, 1*time.Minute, func() (bool, error) {
		for condition, status := range conditions {
			var args []string
			if label == "" {
				args = []string{"get", kind, name, "-n", namespace, "-o", fmt.Sprintf("jsonpath='{.status.conditions[?(@.type==\"%s\")].status}'", condition)}
			} else {
				args = []string{"get", kind, "-n", namespace, "--selector", label, "-o", fmt.Sprintf("jsonpath='{.items[0].status.conditions[?(@.type==\"%s\")].status}'", condition)}
			}
			res, err := k8s.RunKubectlAndGetOutputE(t, &k8s.KubectlOptions{}, args...)
			if err != nil {
				return false, err
			}

			if strings.EqualFold(res, fmt.Sprintf("'%s'", status)) {
				return true, nil
			}
		}

		return false, nil
	})
	return err
}

func applyFromPath(t *testing.T, path string, options *k8s.KubectlOptions) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("Error getting path: %v", err)
	}

	k8s.KubectlApply(t, options, absPath)
}
