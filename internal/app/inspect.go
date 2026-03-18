package app

import (
	"context"
	"fmt"

	"github.com/dougdalo/kcdiag/internal/discovery"
	"github.com/dougdalo/kcdiag/pkg/models"
	"github.com/spf13/cobra"
)

var inspectWorkerCmd = &cobra.Command{
	Use:   "inspect-worker <worker-id-or-pod-name>",
	Short: "Inspect a specific worker in detail",
	Args:  cobra.ExactArgs(1),
	RunE:  runInspectWorker,
}

var inspectConnectorCmd = &cobra.Command{
	Use:   "inspect-connector <connector-name>",
	Short: "Inspect a specific connector in detail",
	Args:  cobra.ExactArgs(1),
	RunE:  runInspectConnector,
}

func runInspectWorker(cmd *cobra.Command, args []string) error {
	target := args[0]
	ctx, cancel := context.WithTimeout(context.Background(), suspectTimeout())
	defer cancel()

	k, err := newK8sClient()
	if err != nil {
		return err
	}

	pods, err := k.DiscoverConnectPods(ctx)
	if err != nil {
		return err
	}
	_ = k.GetPodMetrics(ctx, pods)

	cc := newConnectClient(k)
	connectors := fetchAllConnectors(ctx, cc, pods)
	workers := discovery.BuildWorkers(pods, connectors, cfg.ConnectPort)

	// Find by worker_id or pod name
	var found *models.WorkerInfo
	for i := range workers {
		if workers[i].WorkerID == target {
			found = &workers[i]
			break
		}
		if workers[i].Pod != nil && workers[i].Pod.Name == target {
			found = &workers[i]
			break
		}
	}

	if found == nil {
		return fmt.Errorf("worker %q not found (try worker_id like 10.0.1.5:8083 or pod name)", target)
	}

	return newFormatter().PrintWorkers([]models.WorkerInfo{*found})
}

func runInspectConnector(cmd *cobra.Command, args []string) error {
	target := args[0]
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	k, err := newK8sClient()
	if err != nil {
		return err
	}

	pods, err := k.DiscoverConnectPods(ctx)
	if err != nil {
		return err
	}

	cc := newConnectClient(k)

	// Try each pod until we find the connector
	for _, pod := range pods {
		ref := podRef(pod)

		info, err := cc.GetConnectorStatus(ctx, ref, target)
		if err != nil {
			continue
		}

		cfgMap, _ := cc.GetConnectorConfig(ctx, ref, target)
		if cfgMap != nil {
			info.Config = cfgMap
			info.ClassName = cfgMap["connector.class"]
		}

		return newFormatter().PrintConnectorDetail(info)
	}

	return fmt.Errorf("connector %q not found on any reachable cluster", target)
}
