// Package metrics abstracts connector-level metrics collection.
// Implementations fetch from Prometheus HTTP API or direct /metrics scraping.
package metrics

import (
	"context"
	"strconv"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

// Provider is the interface for fetching connector-level metrics.
type Provider interface {
	// GetConnectorMetrics returns metrics for a specific connector/task.
	GetConnectorMetrics(
		ctx context.Context, connectorName string, taskID int,
	) (*models.ConnectorMetrics, error)

	// GetAllMetrics returns all connector metrics reachable from a pod URL.
	// Key format: "connector-name/taskID"
	GetAllMetrics(ctx context.Context, podURL string) (map[string]*models.ConnectorMetrics, error)

	// Available reports whether this metrics source is reachable.
	Available(ctx context.Context) bool
}

// MetricsKey returns the canonical key for a connector+task pair.
func MetricsKey(connectorName string, taskID int) string {
	return connectorName + "/" + strconv.Itoa(taskID)
}
