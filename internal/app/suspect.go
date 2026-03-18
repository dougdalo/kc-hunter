package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/dougdalo/kcdiag/internal/discovery"
	"github.com/dougdalo/kcdiag/pkg/models"
	"github.com/spf13/cobra"
)

var suspectCmd = &cobra.Command{
	Use:   "suspect",
	Short: "Rank connectors by suspicion of causing memory pressure",
	Long: `The main diagnostic command. Correlates pod memory usage, connector/task
state, and optional Prometheus/JMX metrics to produce a ranked suspect report.

Works even without Prometheus — falls back to K8s + Connect REST signals.`,
	RunE: runSuspect,
}

func runSuspect(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), suspectTimeout())
	defer cancel()

	// 1. Discover pods
	k, err := newK8sClient()
	if err != nil {
		return err
	}

	pods, err := k.DiscoverConnectPods(ctx)
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}
	if len(pods) == 0 {
		fmt.Println("No Kafka Connect pods found.")
		return nil
	}

	// 2. Pod metrics (best-effort)
	_ = k.GetPodMetrics(ctx, pods)

	// 3. Group by Strimzi cluster
	clusters := discovery.GroupPodsByClusters(pods)

	// 4. Set up providers
	mp := newMetricsProvider()
	cc := newConnectClient(k)
	se := newScoringEngine()
	fmtr := newFormatter()

	// 5. Analyze each cluster independently
	for clusterName, clusterPods := range clusters {
		var connectors []models.ConnectorInfo
		queried := false

		// Query one pod per cluster — Connect REST returns cluster-wide state
		for _, pod := range clusterPods {
			ref := podRef(pod)

			connectors, err = cc.GetAllConnectors(ctx, ref)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: cluster %s via pod %s: %v\n", clusterName, pod.Name, err)
				continue
			}
			queried = true
			break
		}
		if !queried {
			fmt.Fprintf(os.Stderr, "warning: no Connect API reachable for cluster %s\n", clusterName)
			continue
		}

		// Collect metrics if provider is available
		allMetrics := make(map[string]*models.ConnectorMetrics)
		if mp.Available(ctx) {
			for _, pod := range clusterPods {
				m, err := mp.GetAllMetrics(ctx, pod.ConnectURL)
				if err != nil {
					continue
				}
				for k, v := range m {
					allMetrics[k] = v
				}
			}
		}

		// Score all tasks
		suspects := se.ScoreAll(clusterPods, connectors, allMetrics)

		// Find hottest pod
		sort.Slice(clusterPods, func(i, j int) bool {
			return clusterPods[i].MemoryUsage > clusterPods[j].MemoryUsage
		})
		var hottest *models.PodInfo
		if len(clusterPods) > 0 {
			hottest = &clusterPods[0]
		}

		diag := models.ClusterDiagnostic{
			ClusterName: clusterName,
			Pods:        clusterPods,
			HottestPod:  hottest,
			Workers:     discovery.BuildWorkers(clusterPods, connectors, cfg.ConnectPort),
			Suspects:    suspects,
			CollectedAt: time.Now(),
		}

		if err := fmtr.PrintSuspects(diag, cfg.TopN); err != nil {
			return err
		}
	}

	return nil
}
