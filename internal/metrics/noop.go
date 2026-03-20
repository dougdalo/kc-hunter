package metrics

import (
	"context"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

// NoopProvider is used when no metrics source is configured.
// The scoring engine still works using K8s + Connect REST signals alone.
type NoopProvider struct{}

func NewNoopProvider() *NoopProvider { return &NoopProvider{} }

func (n *NoopProvider) Available(_ context.Context) bool { return false }

func (n *NoopProvider) GetConnectorMetrics(
	_ context.Context, name string, taskID int,
) (*models.ConnectorMetrics, error) {
	return &models.ConnectorMetrics{
		ConnectorName: name,
		TaskID:        taskID,
		Raw:           map[string]float64{},
	}, nil
}

func (n *NoopProvider) GetAllMetrics(
	_ context.Context, _ string,
) (map[string]*models.ConnectorMetrics, error) {
	return map[string]*models.ConnectorMetrics{}, nil
}
