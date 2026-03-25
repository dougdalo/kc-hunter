// Package app defines the Cobra CLI commands for kc-hunter.
// Each command file uses shared initialization from this root.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/dougdalo/kc-hunter/internal/config"
	"github.com/dougdalo/kc-hunter/internal/connect"
	"github.com/dougdalo/kc-hunter/internal/k8s"
	"github.com/dougdalo/kc-hunter/internal/metrics"
	"github.com/dougdalo/kc-hunter/internal/output"
	"github.com/dougdalo/kc-hunter/internal/scoring"
	"github.com/dougdalo/kc-hunter/pkg/models"
	"github.com/spf13/cobra"
)

// Version information — set via ldflags at build time.
// Example: go build -ldflags "-X ...app.version=1.0.0 -X ...app.commit=abc123"
var (
	version = "dev"
	commit  = "unknown"
)

var (
	cfg        *config.Config
	configPath string
	debugMode  bool
	quietMode  bool
)

var rootCmd = &cobra.Command{
	Use:   "kc-hunter",
	Short: "Kafka Connect memory diagnostics for Strimzi on Kubernetes",
	Long: `kc-hunter correlates pod-level resource metrics, Kafka Connect REST API
state, and optional Prometheus/JMX metrics to identify which connectors
are most likely causing memory pressure in Strimzi Kafka Connect clusters.

It never claims exact per-connector memory — instead it ranks suspects
using indirect evidence signals.

By default, Connect REST calls are made via exec into pods (curl/wget
on localhost), which bypasses networking issues (e.g. GKE timeouts).
Use --use-proxy to route through the K8s API server proxy instead.

Run without arguments to launch an interactive guided wizard.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		installForceQuit()
		return loadConfigFile(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInteractive()
	},
}

func init() {
	cfg = config.DefaultConfig()

	f := rootCmd.PersistentFlags()
	f.StringVar(&configPath, "config", "", "path to config file (YAML)")
	f.StringVar(&cfg.Kubeconfig, "kubeconfig", "",
		"path to kubeconfig (precedence: flag > $KUBECONFIG > ~/.kube/config > in-cluster)")
	f.StringSliceVarP(&cfg.Namespaces, "namespace", "n",
		cfg.Namespaces, "namespace(s) to scan")
	f.StringVarP(&cfg.Labels, "selector", "l",
		cfg.Labels, "label selector for Connect pods")
	f.StringVarP(&cfg.OutputFormat, "output", "o",
		cfg.OutputFormat, "output format: table or json")
	f.DurationVar(&cfg.Timeout, "timeout",
		cfg.Timeout, "per-request timeout")
	f.IntVar(&cfg.TopN, "top",
		cfg.TopN, "number of top suspects to show")
	f.StringVar(&cfg.PrometheusURL, "prometheus-url",
		"", "Prometheus base URL")
	f.StringVar(&cfg.MetricsSource, "metrics",
		cfg.MetricsSource, "metrics source: prometheus, scrape, or none")
	f.StringSliceVar(&cfg.ConnectURLs, "connect-url",
		nil, "explicit Connect REST URL(s); auto-discovered if empty")
	f.IntVar(&cfg.ConnectPort, "connect-port",
		cfg.ConnectPort, "Kafka Connect REST port")
	f.IntVar(&cfg.MetricsPort, "metrics-port",
		cfg.MetricsPort, "JMX exporter metrics port")
	f.IntVar(&cfg.Concurrency, "concurrency",
		cfg.Concurrency, "parallel Connect REST requests")
	f.BoolVar(&cfg.UseProxy, "use-proxy",
		cfg.UseProxy, "route Connect REST calls through K8s API server proxy instead of exec")
	f.BoolVar(&cfg.Explain, "explain",
		false, "show detailed score breakdown for each suspect")
	f.BoolVar(&debugMode, "debug",
		false, "enable debug logging to stderr")
	f.BoolVarP(&quietMode, "quiet", "q",
		false, "suppress informational messages; only show data and errors")

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(podsCmd)
	rootCmd.AddCommand(workersCmd)
	rootCmd.AddCommand(connectorsCmd)
	rootCmd.AddCommand(suspectCmd)
	rootCmd.AddCommand(inspectWorkerCmd)
	rootCmd.AddCommand(inspectConnectorCmd)
	rootCmd.AddCommand(deepInspectCmd)
	rootCmd.AddCommand(snapshotCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(connectorLogsCmd)
}

// loadConfigFile implements merge priority: defaults < config file < flags.
// Cobra has already parsed flags into cfg (which started as defaults).
// We snapshot those values, load the file on top of defaults, then
// re-apply any flags the user explicitly set.
//
// Validation always runs — it catches bad flag values even without a config file.
func loadConfigFile(cmd *cobra.Command, args []string) error {
	initLogger(debugMode)

	if configPath != "" {
		// When the user explicitly passes --config, the file must exist.
		if _, err := os.Stat(configPath); err != nil {
			return fmt.Errorf("config file: %w", err)
		}

		// Snapshot current cfg — contains defaults overridden by any explicit flags.
		flagSnapshot := *cfg

		fileCfg, err := config.LoadFromFile(configPath)
		if err != nil {
			return fmt.Errorf("config file %s: %w", configPath, err)
		}
		*cfg = *fileCfg

		// Re-apply flags that the user explicitly set (flags > file).
		applyFlagOverrides(cmd, &flagSnapshot)
	}

	return cfg.Validate()
}

func applyFlagOverrides(cmd *cobra.Command, snap *config.Config) {
	f := cmd.Flags()
	if f.Changed("kubeconfig") {
		cfg.Kubeconfig = snap.Kubeconfig
	}
	if f.Changed("namespace") {
		cfg.Namespaces = snap.Namespaces
	}
	if f.Changed("selector") {
		cfg.Labels = snap.Labels
	}
	if f.Changed("output") {
		cfg.OutputFormat = snap.OutputFormat
	}
	if f.Changed("timeout") {
		cfg.Timeout = snap.Timeout
	}
	if f.Changed("top") {
		cfg.TopN = snap.TopN
	}
	if f.Changed("prometheus-url") {
		cfg.PrometheusURL = snap.PrometheusURL
	}
	if f.Changed("metrics") {
		cfg.MetricsSource = snap.MetricsSource
	}
	if f.Changed("connect-url") {
		cfg.ConnectURLs = snap.ConnectURLs
	}
	if f.Changed("connect-port") {
		cfg.ConnectPort = snap.ConnectPort
	}
	if f.Changed("metrics-port") {
		cfg.MetricsPort = snap.MetricsPort
	}
	if f.Changed("concurrency") {
		cfg.Concurrency = snap.Concurrency
	}
	if f.Changed("use-proxy") {
		cfg.UseProxy = snap.UseProxy
	}
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print kc-hunter version and build info",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(os.Stdout, "kc-hunter %s (commit: %s)\n", version, commit)
	},
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// initLogger configures the global slog logger.
// Default: warnings only. --debug: all levels including debug.
// Output goes to stderr so it never mixes with data on stdout.
func initLogger(debug bool) {
	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
		// Strip timestamp — noisy for a CLI tool.
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	slog.SetDefault(slog.New(h))
}

// --- output helpers ---

// info prints an informational message to stderr.
// Suppressed by --quiet. Never goes to stdout (which is reserved for data).
func info(format string, args ...any) {
	if !quietMode {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

// warn prints a warning message to stderr.
// Always shown, even with --quiet, because warnings indicate data gaps
// that affect output interpretation.
func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
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

	switch {
	case cfg.UseProxy:
		slog.Debug("transport: proxy via K8s API server")
		transport = connect.NewProxyTransport(k8sClient.ProxyGet, cfg.ConnectPort)
	case len(cfg.ConnectURLs) > 0:
		slog.Debug("transport: direct HTTP", "urls", cfg.ConnectURLs)
		transport = connect.NewDirectTransport(cfg.Timeout)
	default:
		slog.Debug("transport: exec into pods")
		adapter := &execAdapter{client: k8sClient}
		transport = connect.NewExecTransport(adapter.exec, cfg.ConnectPort, "")
	}

	return connect.NewClient(transport, cfg.Concurrency)
}

// execAdapter bridges k8s.Client.ExecInPod to the connect.ExecFunc signature.
type execAdapter struct {
	client *k8s.Client
}

func (a *execAdapter) exec(
	ctx context.Context,
	namespace, podName, container string,
	command []string,
) (string, string, error) {
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
			slog.Warn("--prometheus-url required for prometheus metrics source, falling back to none")
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
	return scoring.NewEngine(cfg.Scoring, cfg.ConnectPort)
}

func newFormatter() *output.Formatter {
	f := output.NewFormatter(cfg.OutputFormat, os.Stdout)
	f.SetExplain(cfg.Explain, cfg.Scoring)
	return f
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
