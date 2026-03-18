// Package app defines the Cobra CLI commands for kcdiag.
// Each command file uses shared initialization from this root.
package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dougdalo/kcdiag/internal/config"
	"github.com/dougdalo/kcdiag/internal/connect"
	"github.com/dougdalo/kcdiag/internal/k8s"
	"github.com/dougdalo/kcdiag/internal/metrics"
	"github.com/dougdalo/kcdiag/internal/output"
	"github.com/dougdalo/kcdiag/internal/scoring"
	"github.com/dougdalo/kcdiag/pkg/models"
	"github.com/spf13/cobra"
)

var cfg *config.Config

var rootCmd = &cobra.Command{
	Use:   "kcdiag",
	Short: "Kafka Connect memory diagnostics for Strimzi on Kubernetes",
	Long: `kcdiag correlates pod-level resource metrics, Kafka Connect REST API
state, and optional Prometheus/JMX metrics to identify which connectors
are most likely causing memory pressure in Strimzi Kafka Connect clusters.

It never claims exact per-connector memory — instead it ranks suspects
using indirect evidence signals.

By default, Connect REST calls are made via exec into pods (curl/wget
on localhost), which bypasses networking issues (e.g. GKE timeouts).
Use --use-proxy to route through the K8s API server proxy instead.

Run without arguments to launch an interactive guided wizard.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInteractive()
	},
}

func init() {
	cfg = config.DefaultConfig()

	f := rootCmd.PersistentFlags()
	f.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "path to kubeconfig (precedence: flag > $KUBECONFIG > ~/.kube/config > in-cluster)")
	f.StringSliceVarP(&cfg.Namespaces, "namespace", "n", cfg.Namespaces, "namespace(s) to scan")
	f.StringVarP(&cfg.Labels, "selector", "l", cfg.Labels, "label selector for Connect pods")
	f.StringVarP(&cfg.OutputFormat, "output", "o", cfg.OutputFormat, "output format: table or json")
	f.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "per-request timeout")
	f.IntVar(&cfg.TopN, "top", cfg.TopN, "number of top suspects to show")
	f.StringVar(&cfg.PrometheusURL, "prometheus-url", "", "Prometheus base URL")
	f.StringVar(&cfg.MetricsSource, "metrics", cfg.MetricsSource, "metrics source: prometheus, scrape, or none")
	f.StringSliceVar(&cfg.ConnectURLs, "connect-url", nil, "explicit Connect REST URL(s); auto-discovered if empty")
	f.IntVar(&cfg.ConnectPort, "connect-port", cfg.ConnectPort, "Kafka Connect REST port")
	f.IntVar(&cfg.MetricsPort, "metrics-port", cfg.MetricsPort, "JMX exporter metrics port")
	f.IntVar(&cfg.Concurrency, "concurrency", cfg.Concurrency, "parallel Connect REST requests")
	f.BoolVar(&cfg.UseProxy, "use-proxy", cfg.UseProxy, "route Connect REST calls through K8s API server proxy instead of exec")

	rootCmd.AddCommand(podsCmd)
	rootCmd.AddCommand(workersCmd)
	rootCmd.AddCommand(connectorsCmd)
	rootCmd.AddCommand(suspectCmd)
	rootCmd.AddCommand(inspectWorkerCmd)
	rootCmd.AddCommand(inspectConnectorCmd)
	rootCmd.AddCommand(deepInspectCmd)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// --- shared helpers used by multiple commands ---

func newK8sClient() (*k8s.Client, error) {
	return k8s.NewClient(cfg)
}

// newConnectClient creates a Connect client with the appropriate transport.
// Default: ExecTransport (execs curl/wget inside pods, bypasses GKE routing).
// --use-proxy: routes through the K8s API server proxy.
// --use-proxy=false --connect-url: direct HTTP to pod IPs.
func newConnectClient(k8sClient *k8s.Client) *connect.Client {
	var transport connect.Transport

	if cfg.UseProxy {
		transport = connect.NewProxyTransport(k8sClient.ProxyGet, cfg.ConnectPort)
	} else if len(cfg.ConnectURLs) > 0 {
		transport = connect.NewDirectTransport(cfg.Timeout)
	} else {
		// Default: exec-based transport — works everywhere including GKE.
		adapter := &execAdapter{client: k8sClient}
		transport = connect.NewExecTransport(adapter.exec, cfg.ConnectPort, "")
	}

	return connect.NewClient(transport, cfg.Concurrency)
}

// execAdapter bridges k8s.Client.ExecInPod to the connect.ExecFunc signature.
type execAdapter struct {
	client *k8s.Client
}

func (a *execAdapter) exec(ctx context.Context, namespace, podName, container string, command []string) (string, string, error) {
	result, err := a.client.ExecInPod(ctx, namespace, podName, container, command)
	if err != nil {
		stdout, stderr := "", ""
		if result != nil {
			stdout = result.Stdout
			stderr = result.Stderr
		}
		return stdout, stderr, err
	}
	return result.Stdout, result.Stderr, nil
}

// podRef builds a PodRef for a given pod. In proxy mode the pod identity
// (name + namespace) is used; in direct mode the pod URL is used.
func podRef(pod models.PodInfo) connect.PodRef {
	return connect.PodRef{
		Name:      pod.Name,
		Namespace: pod.Namespace,
		URL:       pod.ConnectURL,
	}
}

func newMetricsProvider() metrics.Provider {
	switch cfg.MetricsSource {
	case "prometheus":
		if cfg.PrometheusURL == "" {
			fmt.Fprintln(os.Stderr, "warning: --prometheus-url required for prometheus metrics source, falling back to none")
			return metrics.NewNoopProvider()
		}
		return metrics.NewPrometheusProvider(cfg.PrometheusURL, cfg.Timeout)
	case "scrape":
		return metrics.NewScrapeProvider(cfg.MetricsPort, cfg.Timeout)
	default:
		return metrics.NewNoopProvider()
	}
}

func newScoringEngine() *scoring.Engine {
	return scoring.NewEngine(scoring.DefaultThresholds(), cfg.ConnectPort)
}

func newFormatter() *output.Formatter {
	return output.NewFormatter(cfg.OutputFormat, os.Stdout)
}

// suspectTimeout returns a longer timeout for the full diagnostic scan,
// since it queries every connector across all clusters.
func suspectTimeout() time.Duration {
	t := cfg.Timeout * 5
	if t < 2*time.Minute {
		t = 2 * time.Minute
	}
	return t
}
