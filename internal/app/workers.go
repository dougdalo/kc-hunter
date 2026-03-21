package app

import (
	"context"
	"fmt"

	"github.com/dougdalo/kc-hunter/internal/connect"
	"github.com/dougdalo/kc-hunter/internal/discovery"
	"github.com/dougdalo/kc-hunter/pkg/models"
	"github.com/spf13/cobra"
)

var workersCmd = &cobra.Command{
	Use:   "workers",
	Short: "Show worker-to-connector/task mapping",
	RunE:  runWorkers,
}

func runWorkers(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), suspectTimeout())
	defer cancel()

	k, err := newK8sClient()
	if err != nil {
		return err
	}

	pods, err := k.DiscoverConnectPods(ctx)
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}
	_ = k.GetPodMetrics(ctx, pods)

	cc := newConnectClient(k)
	connectors := fetchAllConnectors(ctx, cc, pods)

	workers := discovery.BuildWorkers(pods, connectors, cfg.ConnectPort)
	return newFormatter().PrintWorkers(workers)
}

// fetchAllConnectors queries connector state from one pod per Strimzi cluster.
// The Connect REST API returns cluster-wide state regardless of which pod
// you query, so one successful call per cluster is sufficient.
func fetchAllConnectors(
	ctx context.Context,
	cc *connect.Client,
	pods []models.PodInfo,
) []models.ConnectorInfo {
	var all []models.ConnectorInfo
	queried := make(map[string]bool)

	for _, pod := range pods {
		cluster := pod.ClusterName
		if queried[cluster] {
			continue
		}

		ref := podRef(pod)

		result, err := cc.GetAllConnectors(ctx, ref)
		if err != nil {
			warn("cluster %s via pod %s: %v", cluster, pod.Name, err)
			continue
		}
		if result.Errors > 0 {
			warn("%d/%d connector fetches failed for cluster %s",
				result.Errors, result.Total, cluster)
		}

		all = append(all, result.Connectors...)
		queried[cluster] = true
	}

	return all
}
