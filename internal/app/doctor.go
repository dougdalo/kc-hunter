package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dougdalo/kc-hunter/internal/connect"
	"github.com/dougdalo/kc-hunter/internal/doctor"
	"github.com/dougdalo/kc-hunter/internal/k8s"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Validate cluster access, pods, metrics, and Connect REST prerequisites",
	Long: `Runs a sequence of diagnostic checks to verify that kc-hunter can operate
correctly in the current environment. Useful for troubleshooting before running
suspect reports, or as a first step during an incident.

Checks performed:
  1. Kubernetes API server access
  2. Namespace existence
  3. Kafka Connect pod discovery
  4. metrics-server availability
  5. Connect REST API reachability (via configured transport)
  6. Metrics provider reachability (if configured)
  7. Exec permissions (for exec transport / deep-inspect)
  8. Strimzi replication factor mismatch (detects rebalance loops)`,
	RunE: runDoctor,
}

func runDoctor(cmd *cobra.Command, args []string) error {
	ctx, cancel := signalContext(cfg.Timeout * 3)
	defer cancel()

	report := doctor.Report{
		Timestamp: time.Now(),
	}

	// Determine transport mode for reporting.
	switch {
	case cfg.UseProxy:
		report.Transport = "proxy"
	case len(cfg.ConnectURLs) > 0:
		report.Transport = "direct"
	default:
		report.Transport = "exec"
	}

	// 1. Cluster access.
	k, k8sErr := newK8sClient()
	if k8sErr != nil {
		report.Results = append(report.Results, doctor.CheckResult{
			Name:        "cluster-access",
			Status:      doctor.StatusFail,
			Message:     fmt.Sprintf("cannot create K8s client: %v", k8sErr),
			Remediation: "check --kubeconfig, $KUBECONFIG, or in-cluster config",
		})
		// Cannot proceed without a K8s client.
		report.Results = append(report.Results, skipRemaining("cluster-access failed")...)
		printReport(&report)
		return &SilentError{Code: ExitPartial}
	}

	clusterCheck := doctor.CheckClusterAccess(ctx, func(ctx context.Context) (string, error) {
		sv, err := k.ServerVersion(ctx)
		if err != nil {
			return "", err
		}
		return sv, nil
	})
	report.Results = append(report.Results, clusterCheck)

	if clusterCheck.Status == doctor.StatusFail {
		report.Results = append(report.Results, skipRemaining("cluster-access failed")...)
		printReport(&report)
		return &SilentError{Code: ExitPartial}
	}

	// 2. Namespaces.
	nsCheck := doctor.CheckNamespaces(ctx, cfg.Namespaces, func(ctx context.Context) ([]string, error) {
		return k.ListNamespaces(ctx)
	})
	report.Results = append(report.Results, nsCheck)

	// 3. Pod discovery.
	podResult := doctor.CheckPodDiscovery(ctx, cfg.Labels, func(ctx context.Context) ([]doctor.PodSummary, error) {
		pods, err := k.DiscoverConnectPods(ctx)
		if err != nil {
			return nil, err
		}
		summaries := make([]doctor.PodSummary, len(pods))
		for i, p := range pods {
			summaries[i] = doctor.PodSummary{
				Name:      p.Name,
				Namespace: p.Namespace,
				IP:        p.IP,
				Ready:     p.Ready,
			}
		}
		return summaries, nil
	})
	report.Results = append(report.Results, podResult.Check)

	// 4. metrics-server.
	msCheck := doctor.CheckMetricsServer(ctx, func(ctx context.Context) error {
		return k.ProbeMetricsServer(ctx)
	})
	report.Results = append(report.Results, msCheck)

	// 5. Connect REST — only if pods were discovered.
	if podResult.PodCount > 0 {
		cc := newConnectClient(k)
		connectCheck := doctor.CheckConnectREST(ctx, report.Transport, func(ctx context.Context) (int, error) {
			// Try the first discovered pod.
			pod := podResult.Pods[0]
			ref := connectPodRef(pod)
			names, err := cc.ListConnectors(ctx, ref)
			if err != nil {
				return 0, err
			}
			return len(names), nil
		})
		report.Results = append(report.Results, connectCheck)
	} else {
		report.Results = append(report.Results, doctor.CheckResult{
			Name:    "connect-rest",
			Status:  doctor.StatusSkip,
			Message: "skipped: no pods discovered",
		})
	}

	// 6. Metrics provider.
	mp := newMetricsProvider()
	mpCheck := doctor.CheckMetricsProvider(ctx, cfg.MetricsSource, mp.Available)
	report.Results = append(report.Results, mpCheck)

	// 7. Exec permissions — only relevant for exec transport or deep-inspect.
	if report.Transport == "exec" && podResult.PodCount > 0 {
		execCheck := doctor.CheckExecPermissions(ctx, func(ctx context.Context) error {
			pod := podResult.Pods[0]
			_, err := k.ExecInPod(ctx, pod.Namespace, pod.Name, "", []string{"echo", "ok"})
			return err
		})
		report.Results = append(report.Results, execCheck)
	} else if report.Transport != "exec" {
		report.Results = append(report.Results, doctor.CheckResult{
			Name:    "exec-permissions",
			Status:  doctor.StatusSkip,
			Message: fmt.Sprintf("skipped: not using exec transport (using %s)", report.Transport),
		})
	}

	// 8. Strimzi replication factor mismatch.
	replCheck := doctor.CheckReplicationFactor(ctx, func(ctx context.Context) ([]doctor.ReplicationFactorInfo, error) {
		return fetchReplicationInfos(ctx, k)
	})
	report.Results = append(report.Results, replCheck)

	printReport(&report)

	// Return exit code 2 when checks fail, so scripts can gate on `doctor`.
	// The report was already printed — use SilentError to avoid double-printing.
	if report.HasFailures() {
		return &SilentError{Code: ExitPartial}
	}
	return nil
}

// connectPodRef builds a connect.PodRef from a doctor.PodSummary.
func connectPodRef(p doctor.PodSummary) connect.PodRef {
	ref := connect.PodRef{
		Name:      p.Name,
		Namespace: p.Namespace,
	}
	if p.IP != "" {
		ref.URL = fmt.Sprintf("http://%s:%d", p.IP, cfg.ConnectPort)
	}
	return ref
}

func printReport(report *doctor.Report) {
	fmtr := newFormatter()
	colorize := fmtr.DoctorColorize()

	report.PrintTable(os.Stdout, colorize)
}

// fetchReplicationInfos queries Strimzi CRDs across all configured namespaces
// and converts k8s types to doctor types.
func fetchReplicationInfos(ctx context.Context, k *k8s.Client) ([]doctor.ReplicationFactorInfo, error) {
	namespaces := cfg.Namespaces
	if len(namespaces) == 0 || (len(namespaces) == 1 && namespaces[0] == "") {
		namespaces = []string{""}
	}

	var all []doctor.ReplicationFactorInfo
	for _, ns := range namespaces {
		infos, err := k.GetStrimziReplicationInfo(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, info := range infos {
			var mismatches []doctor.ReplicationMismatchDetail
			for _, m := range info.Mismatches() {
				mismatches = append(mismatches, doctor.ReplicationMismatchDetail{
					FactorName: m.FactorName,
					Value:      m.Value,
					Brokers:    m.Brokers,
				})
			}
			all = append(all, doctor.ReplicationFactorInfo{
				KafkaName:     info.KafkaName,
				KafkaReplicas: info.KafkaReplicas,
				ConnectName:   info.ConnectName,
				Mismatches:    mismatches,
			})
		}
	}
	return all, nil
}

func skipRemaining(reason string) []doctor.CheckResult {
	names := []string{"namespaces", "pod-discovery", "metrics-server", "connect-rest", "metrics-provider", "exec-permissions", "replication-factor"}
	results := make([]doctor.CheckResult, len(names))
	for i, name := range names {
		results[i] = doctor.CheckResult{
			Name:    name,
			Status:  doctor.StatusSkip,
			Message: fmt.Sprintf("skipped: %s", reason),
		}
	}
	return results
}
