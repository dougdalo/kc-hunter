package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/dougdalo/kc-hunter/internal/connect"
	"github.com/dougdalo/kc-hunter/internal/discovery"
	"github.com/dougdalo/kc-hunter/internal/resilience"
	"github.com/dougdalo/kc-hunter/pkg/models"
	"github.com/spf13/cobra"
)

var suspectCmd = &cobra.Command{
	Use:   "suspect",
	Short: "Rank connectors by suspicion of causing memory pressure",
	Long: `The main diagnostic command. Correlates pod memory usage, connector/task
state, and optional Prometheus/JMX metrics to produce a ranked suspect report.

Works even without Prometheus — falls back to K8s + Connect REST signals.
When data collection is partial, the report indicates reduced confidence
and lists which data sources were unavailable.`,
	RunE: runSuspect,
}

func runSuspect(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), suspectTimeout())
	defer cancel()

	// 1. Discover pods (with step timeout: 20% of budget)
	stepCtx, stepCancel := resilience.StepTimeout(ctx, 0.2, cfg.Timeout)
	k, err := newK8sClient()
	if err != nil {
		stepCancel()
		return err
	}

	pods, err := k.DiscoverConnectPods(stepCtx)
	stepCancel()
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}
	if len(pods) == 0 {
		fmt.Println("No Kafka Connect pods found.")
		return nil
	}

	// 2. Pod metrics (best-effort, with step timeout: 15% of budget)
	podMetricsOK := false
	{
		stepCtx, stepCancel := resilience.StepTimeout(ctx, 0.15, cfg.Timeout)
		metricsErr := k.GetPodMetrics(stepCtx, pods)
		stepCancel()

		if metricsErr == nil {
			// Check if any pod actually got metrics data.
			for _, p := range pods {
				if p.MemoryUsage > 0 {
					podMetricsOK = true
					break
				}
			}
		}
	}

	// 3. Group by Strimzi cluster
	clusters := discovery.GroupPodsByClusters(pods)

	// 4. Set up providers
	mp := newMetricsProvider()
	cc := newConnectClient(k)
	se := newScoringEngine()
	fmtr := newFormatter()

	// 5. Analyze each cluster independently
	for clusterName, clusterPods := range clusters {
		stats := &models.CollectionStats{
			PodsDiscovered: len(clusterPods),
			MetricsSource:  cfg.MetricsSource,
		}

		// Track pod metrics availability.
		if podMetricsOK {
			for _, p := range clusterPods {
				if p.MemoryUsage > 0 {
					stats.PodsWithMetrics++
				}
			}
		}
		if !podMetricsOK {
			stats.Warnings = append(stats.Warnings,
				"pod metrics unavailable: memory% and hottest-pod signal may be inaccurate")
		}

		// Query Connect REST with retry (step timeout: 40% of budget).
		var connectors []models.ConnectorInfo
		var fetchResult connect.FetchResult
		queried := false

		stepCtx, stepCancel := resilience.StepTimeout(ctx, 0.4, cfg.Timeout*2)
		for _, pod := range clusterPods {
			ref := podRef(pod)

			retryResult, retryErr := resilience.Do(stepCtx, resilience.DefaultRetryOpts("connect-rest"), func(ctx context.Context) error {
				var fetchErr error
				fetchResult, fetchErr = cc.GetAllConnectors(ctx, ref)
				return fetchErr
			})

			stats.RetryAttempts += retryResult.Attempts
			stats.Warnings = append(stats.Warnings, retryResult.Warnings...)

			if retryErr != nil {
				stats.Warnings = append(stats.Warnings,
					fmt.Sprintf("cluster %s via pod %s: %v", clusterName, pod.Name, retryErr))
				continue
			}

			connectors = fetchResult.Connectors
			stats.ConnectorTotal = fetchResult.Total
			stats.ConnectorErrors = fetchResult.Errors

			// Surface per-connector warnings (capped).
			if fetchResult.Errors > 0 {
				stats.Warnings = append(stats.Warnings,
					fmt.Sprintf("%d/%d connector status fetches failed", fetchResult.Errors, fetchResult.Total))
				cap := min(len(fetchResult.Warnings), 3)
				stats.Warnings = append(stats.Warnings, fetchResult.Warnings[:cap]...)
				if len(fetchResult.Warnings) > 3 {
					stats.Warnings = append(stats.Warnings,
						fmt.Sprintf("... and %d more connector errors", len(fetchResult.Warnings)-3))
				}
			}

			queried = true
			break
		}
		stepCancel()

		if !queried {
			stats.Confidence = "low"
			stats.Warnings = append(stats.Warnings,
				fmt.Sprintf("Connect REST unreachable for cluster %s — no connectors scored", clusterName))
			fmt.Fprintf(os.Stderr, "warning: no Connect API reachable for cluster %s (tried %d pods)\n",
				clusterName, len(clusterPods))

			// Emit a diagnostic-only report so the user sees the cluster was attempted.
			diag := models.ClusterDiagnostic{
				ClusterName: clusterName,
				Pods:        clusterPods,
				Coverage:    stats,
				CollectedAt: time.Now(),
			}
			if err := fmtr.PrintSuspects(diag, cfg.TopN); err != nil {
				return err
			}
			continue
		}

		// Collect connector-level metrics (step timeout: 20% of budget).
		allMetrics := make(map[string]*models.ConnectorMetrics)
		metricsAvail := mp.Available(ctx)
		if metricsAvail {
			stepCtx, stepCancel := resilience.StepTimeout(ctx, 0.2, cfg.Timeout)
			for _, pod := range clusterPods {
				m, mErr := mp.GetAllMetrics(stepCtx, pod.ConnectURL)
				if mErr != nil {
					stats.Warnings = append(stats.Warnings,
						fmt.Sprintf("metrics collection failed for pod %s: %v", pod.Name, mErr))
					continue
				}
				for k, v := range m {
					allMetrics[k] = v
				}
			}
			stepCancel()
			stats.MetricsCollected = len(allMetrics)
		}

		if cfg.MetricsSource != "none" && !metricsAvail {
			stats.Warnings = append(stats.Warnings,
				fmt.Sprintf("%s metrics provider is not reachable — 5 metrics-based signals will not fire", cfg.MetricsSource))
		}

		// Count tasks and compute evaluable signals.
		totalTasks := 0
		for _, c := range connectors {
			totalTasks += len(c.Tasks)
		}
		stats.TotalTasks = totalTasks
		stats.SignalsEvaluable = computeEvaluableSignals(podMetricsOK, len(allMetrics) > 0)

		// Compute confidence.
		stats.Confidence = computeConfidence(stats)

		// Score all tasks.
		suspects := se.ScoreAll(clusterPods, connectors, allMetrics)

		// Find hottest pod.
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

		diag := models.ClusterDiagnostic{
			ClusterName: clusterName,
			Pods:        clusterPods,
			HottestPod:  hottest,
			Workers:     discovery.BuildWorkers(clusterPods, connectors, cfg.ConnectPort),
			Suspects:    suspects,
			Coverage:    stats,
			CollectedAt: time.Now(),
		}

		if err := fmtr.PrintSuspects(diag, cfg.TopN); err != nil {
			return err
		}
	}

	return nil
}

// computeEvaluableSignals returns how many of the 9 scoring signals can
// produce meaningful results given the available data.
//
// Structural signals (always available with Connect REST):
//
//	task_failed, high_task_count, risky_connector_class = 3
//
// Pod-metrics dependent:
//
//	on_hottest_worker = 1 (needs MemoryPercent to determine if pod is "hot")
//
// Connector-metrics dependent:
//
//	high_poll_time, high_put_time, high_batch_size,
//	high_retry_or_errors, high_offset_commit = 5
func computeEvaluableSignals(podMetrics, connectorMetrics bool) int {
	n := 3 // structural signals always evaluable
	if podMetrics {
		n++ // on_hottest_worker
	}
	if connectorMetrics {
		n += 5 // all metrics-based signals
	}
	return n
}

// computeConfidence derives a confidence level from collection stats.
//
//	"high":    all data sources available, minimal errors
//	"reduced": some data missing (pod metrics or connector metrics),
//	           or partial connector fetch failures
//	"low":     major data gaps (>50% connector errors, or no connectors at all)
func computeConfidence(stats *models.CollectionStats) string {
	// No connectors at all — can't score anything.
	if stats.ConnectorTotal == 0 {
		return "low"
	}

	// More than half the connectors failed to fetch.
	if stats.ConnectorErrors > 0 && float64(stats.ConnectorErrors)/float64(stats.ConnectorTotal) > 0.5 {
		return "low"
	}

	// Full signal coverage with minimal issues.
	if stats.SignalsEvaluable == 9 && stats.ConnectorErrors == 0 {
		return "high"
	}

	return "reduced"
}
