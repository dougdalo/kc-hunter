package app

import (
	"testing"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

func TestComputeEvaluableSignals(t *testing.T) {
	tests := []struct {
		name             string
		podMetrics       bool
		connectorMetrics bool
		want             int
	}{
		{"nothing", false, false, 3},
		{"pod metrics only", true, false, 4},
		{"connector metrics only", false, true, 8},
		{"all data", true, true, 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeEvaluableSignals(tt.podMetrics, tt.connectorMetrics)
			if got != tt.want {
				t.Errorf("computeEvaluableSignals(%v, %v)=%d, want %d",
					tt.podMetrics, tt.connectorMetrics, got, tt.want)
			}
		})
	}
}

func TestComputeConfidence(t *testing.T) {
	tests := []struct {
		name  string
		stats *models.CollectionStats
		want  string
	}{
		{
			name:  "no connectors",
			stats: &models.CollectionStats{ConnectorTotal: 0},
			want:  "low",
		},
		{
			name: "majority connector errors",
			stats: &models.CollectionStats{
				ConnectorTotal:   10,
				ConnectorErrors:  6,
				SignalsEvaluable: 4,
			},
			want: "low",
		},
		{
			name: "all signals no errors",
			stats: &models.CollectionStats{
				ConnectorTotal:   10,
				ConnectorErrors:  0,
				SignalsEvaluable: 9,
			},
			want: "high",
		},
		{
			name: "all signals with some errors",
			stats: &models.CollectionStats{
				ConnectorTotal:   10,
				ConnectorErrors:  2,
				SignalsEvaluable: 9,
			},
			want: "reduced",
		},
		{
			name: "missing metrics signals",
			stats: &models.CollectionStats{
				ConnectorTotal:   10,
				ConnectorErrors:  0,
				SignalsEvaluable: 4,
			},
			want: "reduced",
		},
		{
			name: "exactly 50% errors is not low",
			stats: &models.CollectionStats{
				ConnectorTotal:   10,
				ConnectorErrors:  5,
				SignalsEvaluable: 9,
			},
			want: "reduced",
		},
		{
			name: "51% errors is low",
			stats: &models.CollectionStats{
				ConnectorTotal:   100,
				ConnectorErrors:  51,
				SignalsEvaluable: 9,
			},
			want: "low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeConfidence(tt.stats)
			if got != tt.want {
				t.Errorf("computeConfidence()=%q, want %q", got, tt.want)
			}
		})
	}
}
