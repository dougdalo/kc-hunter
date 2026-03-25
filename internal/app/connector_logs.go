package app

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/AlecAivazis/survey/v2"
	"github.com/dougdalo/kc-hunter/internal/k8s"
	"github.com/dougdalo/kc-hunter/pkg/models"
	"github.com/spf13/cobra"
)

var connectorLogsCmd = &cobra.Command{
	Use:   "connector-logs [connector-name]",
	Short: "Tail logs for a specific connector across all workers",
	Long: `Streams logs from all Kafka Connect pods (workers) and filters lines
that match the selected connector name.

Because Kafka Connect distributes tasks across multiple worker pods,
this command opens concurrent log streams to all pods and merges them
in real time, prefixing each line with the pod name for easy debugging.

If no connector name is provided as an argument, an interactive prompt
lets you pick one from the running connectors.

Press Ctrl+C to stop tailing.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConnectorLogs,
}

var logTailLines int64

func init() {
	connectorLogsCmd.Flags().Int64Var(&logTailLines, "tail", 500,
		"number of historical log lines to fetch per pod before following")
}

func runConnectorLogs(cmd *cobra.Command, args []string) error {
	// Use a long-lived context — log tailing runs until Ctrl+C.
	ctx, cancel := signalContext(24 * 60 * 60 * 1e9) // 24h ceiling; signal cancels it
	defer cancel()

	k, err := newK8sClient()
	if err != nil {
		return err
	}

	// Step 1: Discover Connect pods.
	pods, err := k.DiscoverConnectPods(ctx)
	if err != nil {
		return fmt.Errorf("discover pods: %w", err)
	}
	if len(pods) == 0 {
		return fmt.Errorf("no Kafka Connect pods found")
	}

	// Step 2: Resolve connector name — from arg or interactive selection.
	connectorName, err := resolveConnectorName(ctx, k, pods, args)
	if err != nil {
		return err
	}

	info("Tailing logs for connector %q across %d pod(s)...", connectorName, len(pods))
	info("Press Ctrl+C to stop.\n")

	// Step 3: Fan-in log lines from all pods into a single channel.
	lines := make(chan logLine, 256)

	var wg sync.WaitGroup
	for _, pod := range pods {
		wg.Add(1)
		go func(p models.PodInfo) {
			defer wg.Done()
			streamPodLogs(ctx, k, p, lines)
		}(pod)
	}

	// Close the channel once all goroutines finish (context cancelled).
	go func() {
		wg.Wait()
		close(lines)
	}()

	// Step 4: Consume, filter, and print.
	return filterAndPrint(lines, connectorName)
}

// logLine carries a single log line with its source pod identifier.
type logLine struct {
	pod  string
	text string
}

// resolveConnectorName determines the connector to tail — either from CLI args
// or by presenting an interactive selection from the running connectors.
func resolveConnectorName(
	ctx context.Context,
	k *k8s.Client,
	pods []models.PodInfo,
	args []string,
) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}

	// Fetch connector names from the Connect REST API.
	cc := newConnectClient(k)
	connectors := fetchAllConnectors(ctx, cc, pods)
	if len(connectors) == 0 {
		return "", fmt.Errorf("no connectors found in cluster")
	}

	names := make([]string, len(connectors))
	for i, c := range connectors {
		names[i] = c.Name
	}

	var selected string
	err := survey.AskOne(&survey.Select{
		Message:  "Select connector to tail:",
		Options:  names,
		PageSize: 20,
	}, &selected, survey.WithValidator(survey.Required))
	if err != nil {
		return "", handleInterrupt(err)
	}

	return selected, nil
}

// streamPodLogs opens a follow-mode log stream for a single pod and sends
// each line to the shared channel. It returns when the context is cancelled
// or the stream ends.
func streamPodLogs(
	ctx context.Context,
	k *k8s.Client,
	pod models.PodInfo,
	out chan<- logLine,
) {
	stream, err := k.StreamPodLogs(ctx, pod.Namespace, pod.Name, logTailLines)
	if err != nil {
		slog.Warn("failed to open log stream",
			"pod", pod.Name, "error", err)
		return
	}
	defer stream.Close()

	// Shorten pod name for the prefix: last 12 chars or full name if shorter.
	prefix := podPrefix(pod.Name)

	scanner := bufio.NewScanner(stream)
	// Kafka Connect log lines can be long (stack traces). 1 MB buffer.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		case out <- logLine{pod: prefix, text: scanner.Text()}:
		}
	}

	if err := scanner.Err(); err != nil {
		// Context cancellation is expected (Ctrl+C); don't warn about it.
		if ctx.Err() == nil {
			slog.Warn("log scanner error",
				"pod", pod.Name, "error", err)
		}
	}
}

// filterAndPrint reads from the merged log channel and prints lines
// containing the connector name, prefixed with the pod identifier.
func filterAndPrint(lines <-chan logLine, connectorName string) error {
	for line := range lines {
		if strings.Contains(line.text, connectorName) {
			fmt.Fprintf(os.Stdout, "\033[36m[%s]\033[0m %s\n", line.pod, line.text)
		}
	}
	return nil
}

// podPrefix returns a short identifier from a pod name.
// For a typical Strimzi pod like "kafka-connect-connect-0", it returns
// the last segment after the final hyphen, or the last 12 characters.
func podPrefix(name string) string {
	if idx := strings.LastIndex(name, "-"); idx >= 0 && idx < len(name)-1 {
		suffix := name[idx+1:]
		// If suffix is just a number (StatefulSet ordinal), include more context.
		if len(suffix) <= 3 {
			// Use from the second-to-last hyphen.
			short := name
			if idx2 := strings.LastIndex(name[:idx], "-"); idx2 >= 0 {
				short = name[idx2+1:]
			}
			return short
		}
		return suffix
	}
	if len(name) > 12 {
		return name[len(name)-12:]
	}
	return name
}
