package app

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/AlecAivazis/survey/v2"
	"github.com/dougdalo/kcdiag/pkg/models"
)

const (
	actionSuspect     = "\U0001F50D Suspect Report"
	actionPods        = "\U0001F4CB Pod Overview"
	actionWorkers     = "\U0001F477 Worker Map"
	actionDeepInspect = "\U0001F9EA Deep JVM Inspect"
)

// runInteractive launches the guided TUI wizard when no subcommand is provided.
func runInteractive() error {
	// Step 1: Choose action.
	var action string
	err := survey.AskOne(&survey.Select{
		Message: "What would you like to do?",
		Options: []string{actionSuspect, actionPods, actionWorkers, actionDeepInspect},
	}, &action, survey.WithValidator(survey.Required))
	if err != nil {
		return handleInterrupt(err)
	}

	// Step 2: Build K8s client and fetch namespaces.
	k8sClient, err := newK8sClient()
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	namespaces, err := k8sClient.ListNamespaces(ctx)
	if err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}
	sort.Strings(namespaces)

	if len(namespaces) == 0 {
		return fmt.Errorf("no namespaces found in cluster")
	}

	var namespace string
	err = survey.AskOne(&survey.Select{
		Message:  "Select namespace:",
		Options:  namespaces,
		PageSize: 15,
	}, &namespace, survey.WithValidator(survey.Required))
	if err != nil {
		return handleInterrupt(err)
	}

	// Apply selected namespace to global config.
	cfg.Namespaces = []string{namespace}

	// Step 3: For Deep JVM Inspect, let user pick a specific pod.
	var targetPod string
	if action == actionDeepInspect {
		pods, discoverErr := discoverPodsInNamespace(ctx, k8sClient, namespace)
		if discoverErr != nil {
			return discoverErr
		}

		podNames := make([]string, 0, len(pods)+1)
		podNames = append(podNames, "All Pods")
		for _, p := range pods {
			podNames = append(podNames, p.Name)
		}

		err = survey.AskOne(&survey.Select{
			Message:  "Select pod to inspect:",
			Options:  podNames,
			PageSize: 15,
		}, &targetPod, survey.WithValidator(survey.Required))
		if err != nil {
			return handleInterrupt(err)
		}

		if targetPod == "All Pods" {
			targetPod = ""
		}
	}

	// Step 4: Dispatch to the appropriate command.
	fmt.Fprintln(os.Stdout)
	switch action {
	case actionSuspect:
		return runSuspect(nil, nil)
	case actionPods:
		return runPods(nil, nil)
	case actionWorkers:
		return runWorkers(nil, nil)
	case actionDeepInspect:
		var args []string
		if targetPod != "" {
			args = []string{targetPod}
		}
		return runDeepInspect(nil, args)
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}

// discoverPodsInNamespace finds Kafka Connect pods in the given namespace.
func discoverPodsInNamespace(ctx context.Context, k8sClient interface{ DiscoverConnectPods(context.Context) ([]models.PodInfo, error) }, namespace string) ([]models.PodInfo, error) {
	allPods, err := k8sClient.DiscoverConnectPods(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover pods: %w", err)
	}

	var filtered []models.PodInfo
	for _, p := range allPods {
		if p.Namespace == namespace {
			filtered = append(filtered, p)
		}
	}

	if len(filtered) == 0 {
		return nil, fmt.Errorf("no Kafka Connect pods found in namespace %q", namespace)
	}

	return filtered, nil
}

// handleInterrupt returns nil for user cancellation (Ctrl+C) to exit cleanly.
func handleInterrupt(err error) error {
	if err.Error() == "interrupt" {
		fmt.Fprintln(os.Stderr, "\nAborted.")
		os.Exit(0)
	}
	return err
}
