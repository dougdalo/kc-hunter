package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

var podsCmd = &cobra.Command{
	Use:   "pods",
	Short: "List Kafka Connect pods ranked by memory usage",
	RunE:  runPods,
}

func runPods(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
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
		fmt.Println("No Kafka Connect pods found. Check --namespace and --selector.")
		return nil
	}

	_ = k.GetPodMetrics(ctx, pods) // best-effort

	sort.Slice(pods, func(i, j int) bool {
		return pods[i].MemoryUsage > pods[j].MemoryUsage
	})

	return newFormatter().PrintPods(pods)
}
