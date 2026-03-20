package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/dougdalo/kc-hunter/internal/discovery"
	"github.com/dougdalo/kc-hunter/internal/snapshot"
	"github.com/dougdalo/kc-hunter/pkg/models"
	"github.com/spf13/cobra"
)

var snapshotOutputFile string

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Save and compare cluster state snapshots",
}

var snapshotSaveCmd = &cobra.Command{
	Use:   "save",
	Short: "Capture current cluster state to a JSON file",
	RunE:  runSnapshotSave,
}

var snapshotDiffCmd = &cobra.Command{
	Use:   "diff <before.json> <after.json>",
	Short: "Compare two snapshots and show what changed",
	Args:  cobra.ExactArgs(2),
	RunE:  runSnapshotDiff,
}

func init() {
	snapshotSaveCmd.Flags().StringVarP(&snapshotOutputFile, "output-file", "O",
		"", "output file path (required)")
	_ = snapshotSaveCmd.MarkFlagRequired("output-file")

	snapshotCmd.AddCommand(snapshotSaveCmd)
	snapshotCmd.AddCommand(snapshotDiffCmd)
}

func runSnapshotSave(cmd *cobra.Command, args []string) error {
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
	if len(pods) == 0 {
		return fmt.Errorf("no Kafka Connect pods found")
	}

	_ = k.GetPodMetrics(ctx, pods)

	clusters := discovery.GroupPodsByClusters(pods)

	mp := newMetricsProvider()
	cc := newConnectClient(k)
	se := newScoringEngine()

	var diags []models.ClusterDiagnostic

	for clusterName, clusterPods := range clusters {
		var connectors []models.ConnectorInfo
		queried := false

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

		suspects := se.ScoreAll(clusterPods, connectors, allMetrics)

		sort.Slice(clusterPods, func(i, j int) bool {
			if clusterPods[i].MemoryPercent != clusterPods[j].MemoryPercent {
				return clusterPods[i].MemoryPercent > clusterPods[j].MemoryPercent
			}
			return clusterPods[i].MemoryUsage > clusterPods[j].MemoryUsage
		})
		var hottest *models.PodInfo
		if len(clusterPods) > 0 {
			hottest = &clusterPods[0]
		}

		diags = append(diags, models.ClusterDiagnostic{
			ClusterName: clusterName,
			Pods:        clusterPods,
			HottestPod:  hottest,
			Workers:     discovery.BuildWorkers(clusterPods, connectors, cfg.ConnectPort),
			Suspects:    suspects,
			CollectedAt: time.Now(),
		})
	}

	if len(diags) == 0 {
		return fmt.Errorf("no cluster data collected")
	}

	snap := snapshot.BuildSnapshot(diags)
	if err := snapshot.Save(snap, snapshotOutputFile); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Snapshot saved to %s (%d clusters, %d connectors)\n",
		snapshotOutputFile, len(snap.Clusters), countConnectors(snap))
	return nil
}

func runSnapshotDiff(cmd *cobra.Command, args []string) error {
	before, err := snapshot.Load(args[0])
	if err != nil {
		return err
	}
	after, err := snapshot.Load(args[1])
	if err != nil {
		return err
	}

	diff := snapshot.Diff(before, after)
	fmtr := newFormatter()
	return fmtr.PrintDiff(diff)
}

func countConnectors(snap models.Snapshot) int {
	n := 0
	for _, c := range snap.Clusters {
		n += len(c.Connectors)
	}
	return n
}
