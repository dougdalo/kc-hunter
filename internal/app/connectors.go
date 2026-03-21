package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var connectorsCmd = &cobra.Command{
	Use:   "connectors",
	Short: "List all connectors with status",
	RunE:  runConnectors,
}

func runConnectors(cmd *cobra.Command, args []string) error {
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

	cc := newConnectClient(k)
	connectors := fetchAllConnectors(ctx, cc, pods)

	if len(connectors) == 0 {
		info("No connectors found.")
		return nil
	}

	return newFormatter().PrintConnectors(connectors)
}
