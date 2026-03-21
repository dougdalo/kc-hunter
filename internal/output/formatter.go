// Package output handles rendering diagnostic results as table or JSON.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/dougdalo/kc-hunter/internal/config"
	"github.com/dougdalo/kc-hunter/pkg/models"
)

// Formatter renders diagnostic data.
type Formatter struct {
	format     string
	w          io.Writer
	explain    bool
	scoringCfg config.ScoringConfig
	c          colorizer
}

func NewFormatter(format string, w io.Writer) *Formatter {
	return &Formatter{
		format: format,
		w:      w,
		c:      colorizer{enabled: format != "json" && isTTY(w)},
	}
}

// SetExplain enables detailed score breakdown output.
func (f *Formatter) SetExplain(v bool, scoringCfg config.ScoringConfig) {
	f.explain = v
	f.scoringCfg = scoringCfg
}

// PrintPods renders pod resource usage.
func (f *Formatter) PrintPods(pods []models.PodInfo) error {
	if f.format == "json" {
		return f.json(pods)
	}

	tw := tabwriter.NewWriter(f.w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, f.c.bold("POD\tCLUSTER\tNODE\tMEM USED\tMEM LIMIT\tMEM%\tCPU(m)\tREADY\tRESTARTS"))
	fmt.Fprintln(tw, "---\t-------\t----\t--------\t---------\t----\t------\t-----\t--------")
	for _, p := range pods {
		memStr := f.c.memColor(p.MemoryPercent)(fmt.Sprintf("%.1f%%", p.MemoryPercent))
		readyStr := fmt.Sprintf("%v", p.Ready)
		if !p.Ready {
			readyStr = f.c.boldRed(readyStr)
		}
		restartStr := fmt.Sprintf("%d", p.RestartCount)
		if p.RestartCount > 0 {
			restartStr = f.c.yellow(restartStr)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			p.Name, p.ClusterName, p.NodeName,
			fmtBytes(p.MemoryUsage), fmtBytes(p.MemoryLimit),
			memStr, p.CPUUsage, readyStr, restartStr)
	}
	return tw.Flush()
}

// PrintWorkers renders worker-to-task mapping.
func (f *Formatter) PrintWorkers(workers []models.WorkerInfo) error {
	if f.format == "json" {
		return f.json(workers)
	}

	for _, w := range workers {
		fmt.Fprintf(f.w, "\n--- Worker: %s", f.c.bold(w.WorkerID))
		if w.Pod != nil {
			memPct := fmt.Sprintf("%.1f%%", w.Pod.MemoryPercent)
			fmt.Fprintf(f.w, "  (pod: %s, mem: %s / %s = %s)",
				w.Pod.Name, fmtBytes(w.Pod.MemoryUsage), fmtBytes(w.Pod.MemoryLimit),
				f.c.memColor(w.Pod.MemoryPercent)(memPct))
		}
		fmt.Fprintf(f.w, " ---\nTasks: %d\n\n", w.TaskCount)

		tw := tabwriter.NewWriter(f.w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, f.c.bold("  CONNECTOR\tTASK\tSTATE\tTYPE"))
		fmt.Fprintln(tw, "  ---------\t----\t-----\t----")
		for _, t := range w.Tasks {
			cType := ""
			for _, c := range w.Connectors {
				if c.Name == t.ConnectorName {
					cType = c.Type
					break
				}
			}
			fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\n",
				t.ConnectorName, t.TaskID, f.c.stateColor(t.State)(t.State), cType)
		}
		tw.Flush()
	}
	return nil
}

// PrintConnectors renders a connector listing.
func (f *Formatter) PrintConnectors(connectors []models.ConnectorInfo) error {
	if f.format == "json" {
		return f.json(connectors)
	}

	tw := tabwriter.NewWriter(f.w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, f.c.bold("CONNECTOR\tTYPE\tSTATE\tTASKS\tWORKER\tCLASS"))
	fmt.Fprintln(tw, "---------\t----\t-----\t-----\t------\t-----")
	for _, c := range connectors {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			c.Name, c.Type, f.c.stateColor(c.State)(c.State),
			taskSummary(c.Tasks), c.WorkerID, shortClass(c.ClassName))
	}
	return tw.Flush()
}

// PrintSuspects renders the ranked suspect report.
func (f *Formatter) PrintSuspects(diag models.ClusterDiagnostic, topN int) error {
	if f.format == "json" {
		return f.json(diag)
	}

	c := f.c

	// --- Header ---
	fmt.Fprintf(f.w, "\n%s\n", c.bold("========================================"))
	fmt.Fprintf(f.w, " %s\n", c.bold("SUSPECT REPORT: "+diag.ClusterName))
	fmt.Fprintf(f.w, "%s\n\n", c.bold("========================================"))

	// --- Quick Summary ---
	// Count unique connectors and failed tasks before truncating.
	connectorSet := make(map[string]bool)
	failedCount := 0
	for _, s := range diag.Suspects {
		connectorSet[s.ConnectorName] = true
		for _, sig := range s.Signals {
			if sig.Name == "task_failed" && sig.Active {
				failedCount++
			}
		}
	}

	fmt.Fprintf(f.w, "%s\n", c.bold("  SUMMARY"))
	fmt.Fprintf(f.w, "  Connectors analyzed: %s\n", c.bold(fmt.Sprintf("%d", len(connectorSet))))
	fmt.Fprintf(f.w, "  Tasks analyzed:      %s\n", c.bold(fmt.Sprintf("%d", len(diag.Suspects))))
	if failedCount > 0 {
		fmt.Fprintf(f.w, "  Failed tasks:        %s\n", c.boldRed(fmt.Sprintf("%d", failedCount)))
	} else {
		fmt.Fprintf(f.w, "  Failed tasks:        %s\n", c.boldGreen("0"))
	}

	if p := diag.HottestPod; p != nil {
		memPct := fmt.Sprintf("%.1f%%", p.MemoryPercent)
		fmt.Fprintf(f.w, "  Hottest pod:         %s (%s, node: %s)\n",
			c.bold(p.Name),
			c.memColor(p.MemoryPercent)(fmtBytes(p.MemoryUsage)+"/"+fmtBytes(p.MemoryLimit)+" "+memPct),
			p.NodeName)
	}

	if len(diag.Suspects) > 0 {
		top := diag.Suspects[0]
		fmt.Fprintf(f.w, "  Top suspect:         %s (score: %s)\n",
			c.bold(fmt.Sprintf("%s/task-%d", top.ConnectorName, top.TaskID)),
			c.scoreColor(top.Score)(fmt.Sprintf("%d/100", top.Score)))
	}

	// Coverage / confidence section.
	if cov := diag.Coverage; cov != nil {
		confidenceStr := cov.Confidence
		switch cov.Confidence {
		case "high":
			confidenceStr = c.boldGreen("HIGH")
		case "reduced":
			confidenceStr = c.boldYellow("REDUCED")
		case "low":
			confidenceStr = c.boldRed("LOW")
		}
		fmt.Fprintf(f.w, "  Confidence:          %s (%d/9 signals evaluable)\n",
			confidenceStr, cov.SignalsEvaluable)

		// Show data source summary on one line.
		podMetrics := c.boldGreen("yes")
		if cov.PodsWithMetrics == 0 {
			podMetrics = c.boldRed("no")
		}
		metricsProvider := c.dim("none")
		if cov.MetricsSource != "none" {
			if cov.MetricsCollected > 0 {
				metricsProvider = c.boldGreen(fmt.Sprintf("%s (%d)", cov.MetricsSource, cov.MetricsCollected))
			} else {
				metricsProvider = c.boldRed(cov.MetricsSource + " (unreachable)")
			}
		}
		connErr := ""
		if cov.ConnectorErrors > 0 {
			connErr = c.boldYellow(fmt.Sprintf(" (%d errors)", cov.ConnectorErrors))
		}
		fmt.Fprintf(f.w, "  Data sources:        pod-metrics=%s  connector-metrics=%s  connectors=%d%s\n",
			podMetrics, metricsProvider, cov.ConnectorTotal-cov.ConnectorErrors, connErr)
	}
	fmt.Fprintln(f.w)

	// --- Suspect List ---
	suspects := diag.Suspects
	if topN > 0 && len(suspects) > topN {
		suspects = suspects[:topN]
	}

	for i, s := range suspects {
		// Rank number — highlight #1
		rank := fmt.Sprintf("%d.", i+1)
		if i == 0 {
			rank = c.boldRed(rank)
		}

		fmt.Fprintf(f.w, "  %s %s\n", rank, c.bold(fmt.Sprintf("%s / task-%d", s.ConnectorName, s.TaskID)))
		fmt.Fprintf(f.w, "     Worker: %s (pod: %s)\n", s.WorkerID, s.PodName)
		fmt.Fprintf(f.w, "     Score:  %s\n", c.coloredScoreBar(s.Score))

		if f.explain {
			f.printExplain(s)
		} else {
			if len(s.Reasons) > 0 {
				fmt.Fprintf(f.w, "     Reasons:\n")
				for _, r := range s.Reasons {
					fmt.Fprintf(f.w, "       - %s\n", c.yellow(r))
				}
			}
		}

		if s.Recommendation != "" {
			fmt.Fprintf(f.w, "     Recommendation: %s\n", c.cyan(s.Recommendation))
		}
		fmt.Fprintln(f.w)
	}

	// --- Warnings ---
	if cov := diag.Coverage; cov != nil && len(cov.Warnings) > 0 {
		fmt.Fprintf(f.w, "  %s\n", c.boldYellow("WARNINGS"))
		for _, w := range cov.Warnings {
			fmt.Fprintf(f.w, "  %s %s\n", c.yellow("!"), w)
		}
		fmt.Fprintln(f.w)
	}

	// --- Footer ---
	fmt.Fprintf(f.w, "%s\n", c.dim("----------------------------------------"))
	footer := fmt.Sprintf("Showing top %d of %d tasks", len(suspects), len(diag.Suspects))
	if cov := diag.Coverage; cov != nil && cov.Confidence != "high" {
		footer += fmt.Sprintf(" | confidence: %s — interpret scores with caution", cov.Confidence)
	}
	fmt.Fprintf(f.w, "%s\n", c.dim(footer))

	return nil
}

// PrintConnectorDetail renders detailed info for a single connector.
func (f *Formatter) PrintConnectorDetail(c *models.ConnectorInfo) error {
	if f.format == "json" {
		return f.json(c)
	}

	fmt.Fprintf(f.w, "\nConnector: %s\n", f.c.bold(c.Name))
	fmt.Fprintf(f.w, "Type:      %s\n", c.Type)
	fmt.Fprintf(f.w, "State:     %s\n", f.c.stateColor(c.State)(c.State))
	fmt.Fprintf(f.w, "Worker:    %s\n", c.WorkerID)
	fmt.Fprintf(f.w, "Class:     %s\n", c.ClassName)
	fmt.Fprintf(f.w, "Tasks:     %d\n\n", len(c.Tasks))

	for _, t := range c.Tasks {
		fmt.Fprintf(f.w, "  Task %d: state=%s worker=%s\n",
			t.TaskID, f.c.stateColor(t.State)(t.State), t.WorkerID)
		if t.Trace != "" {
			trace := t.Trace
			if len(trace) > 300 {
				trace = trace[:300] + "..."
			}
			fmt.Fprintf(f.w, "    Error: %s\n", f.c.red(trace))
		}
	}
	return nil
}

// printExplain renders a detailed score breakdown for a single suspect.
func (f *Formatter) printExplain(s models.SuspectReport) {
	fmt.Fprintf(f.w, "     Score Breakdown:\n")

	tw := tabwriter.NewWriter(f.w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "       SIGNAL\tWEIGHT\tACTIVE\tTHRESHOLD\tVALUE\tDESCRIPTION\n")
	fmt.Fprintf(tw, "       ------\t------\t------\t---------\t-----\t-----------\n")

	sc := f.scoringCfg
	thresholds := map[string]string{
		"on_hottest_worker":    fmt.Sprintf(">=%.0f%% mem", sc.MemoryPercentHot),
		"task_failed":          "state=FAILED|UNASSIGNED",
		"high_task_count":      fmt.Sprintf(">=%d tasks", sc.HighTaskCount),
		"risky_connector_class": "class in risky list",
		"high_poll_time":       fmt.Sprintf(">%.0fms", sc.PollTimeHighMs),
		"high_put_time":        fmt.Sprintf(">%.0fms", sc.PutTimeHighMs),
		"high_batch_size":      fmt.Sprintf(">%.0f", sc.BatchSizeHigh),
		"high_retry_or_errors": fmt.Sprintf(">%.0f retries or errors>0", sc.HighRetryCount),
		"high_offset_commit":   fmt.Sprintf(">%.0fms", sc.OffsetCommitHighMs),
	}

	activeTotal := 0
	for _, sig := range s.Signals {
		active := "no"
		value := "-"
		desc := "-"
		if sig.Active {
			active = "YES"
			activeTotal += sig.Weight
			if sig.Value != "" {
				value = sig.Value
			}
			if sig.Description != "" {
				desc = sig.Description
			}
		}
		threshold := thresholds[sig.Name]
		if threshold == "" {
			threshold = "-"
		}
		fmt.Fprintf(tw, "       %s\t%d\t%s\t%s\t%s\t%s\n",
			sig.Name, sig.Weight, active, threshold, value, desc)
	}
	tw.Flush()

	fmt.Fprintf(f.w, "     Raw total: %d (capped at 100)\n", activeTotal)
	fmt.Fprintf(f.w, "     Final score: %d\n", s.Score)

	if len(s.Reasons) > 0 {
		fmt.Fprintf(f.w, "     Reasons:\n")
		for _, r := range s.Reasons {
			fmt.Fprintf(f.w, "       - %s\n", r)
		}
	}
}

// PrintDiff renders a snapshot diff report.
func (f *Formatter) PrintDiff(diff models.DiffReport) error {
	if f.format == "json" {
		return f.json(diff)
	}

	c := f.c
	fmt.Fprintf(f.w, "\n%s\n", c.bold("========================================"))
	fmt.Fprintf(f.w, " %s\n", c.bold("SNAPSHOT DIFF"))
	fmt.Fprintf(f.w, "%s\n", c.bold("========================================"))
	fmt.Fprintf(f.w, " Before: %s\n", diff.BeforeTime.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(f.w, " After:  %s\n", diff.AfterTime.Format("2006-01-02 15:04:05"))

	// Executive summary.
	if s := diff.Summary; s != nil {
		fmt.Fprintf(f.w, " Delta:  %s\n\n", s.TimeDelta)
		fmt.Fprintf(f.w, " %s %s\n\n", c.bold(">>"), c.bold(s.Headline))

		// Counts line.
		var parts []string
		if s.ConnectorsAdded > 0 {
			parts = append(parts, c.green(fmt.Sprintf("+%d connectors", s.ConnectorsAdded)))
		}
		if s.ConnectorsRemoved > 0 {
			parts = append(parts, c.red(fmt.Sprintf("-%d connectors", s.ConnectorsRemoved)))
		}
		if s.ConnectorsChanged > 0 {
			parts = append(parts, c.yellow(fmt.Sprintf("~%d connectors", s.ConnectorsChanged)))
		}
		if s.ScoreIncreases > 0 {
			parts = append(parts, c.boldRed(fmt.Sprintf("%d scores ↑", s.ScoreIncreases)))
		}
		if s.ScoreDecreases > 0 {
			parts = append(parts, c.boldGreen(fmt.Sprintf("%d scores ↓", s.ScoreDecreases)))
		}
		if s.TaskStateChanges > 0 {
			parts = append(parts, fmt.Sprintf("%d task changes", s.TaskStateChanges))
		}
		if s.PodRestartChanges > 0 {
			parts = append(parts, c.boldYellow(fmt.Sprintf("%d pod restarts", s.PodRestartChanges)))
		}
		if s.PodsAdded > 0 {
			parts = append(parts, c.green(fmt.Sprintf("+%d pods", s.PodsAdded)))
		}
		if s.PodsRemoved > 0 {
			parts = append(parts, c.red(fmt.Sprintf("-%d pods", s.PodsRemoved)))
		}
		if len(parts) > 0 {
			fmt.Fprintf(f.w, " %s\n\n", strings.Join(parts, "  "))
		}
	} else {
		fmt.Fprintln(f.w)
	}

	// Metadata diff warning.
	if md := diff.MetaDiff; md != nil {
		fmt.Fprintf(f.w, " %s %s\n", c.boldYellow("!"), c.boldYellow("Collection parameters changed — interpret diff with caution:"))
		for _, ch := range md.Changes {
			fmt.Fprintf(f.w, "   %s\n", ch)
		}
		fmt.Fprintln(f.w)
	}

	hasChanges := false
	for _, cd := range diff.Clusters {
		clusterHasChanges := len(cd.AddedConnectors) > 0 || len(cd.RemovedConnectors) > 0 ||
			len(cd.ChangedConnectors) > 0 || len(cd.AddedSuspects) > 0 ||
			len(cd.RemovedSuspects) > 0 || len(cd.ChangedSuspects) > 0 ||
			len(cd.AddedPods) > 0 || len(cd.RemovedPods) > 0 || len(cd.ChangedPods) > 0

		if !clusterHasChanges {
			continue
		}
		hasChanges = true

		fmt.Fprintf(f.w, "--- Cluster: %s ---\n\n", c.bold(cd.ClusterName))

		// Infrastructure: pod changes.
		if len(cd.AddedPods) > 0 || len(cd.RemovedPods) > 0 || len(cd.ChangedPods) > 0 {
			fmt.Fprintf(f.w, "  %s\n", c.bold("Infrastructure:"))
			for _, p := range cd.AddedPods {
				fmt.Fprintf(f.w, "    %s (node=%s, mem=%.1f%%)\n",
					c.green("+ pod "+p.Name), p.NodeName, p.MemoryPercent)
			}
			for _, p := range cd.RemovedPods {
				fmt.Fprintf(f.w, "    %s (node=%s)\n",
					c.red("- pod "+p.Name), p.NodeName)
			}
			for _, p := range cd.ChangedPods {
				fmt.Fprintf(f.w, "    %s\n", c.yellow("~ pod "+p.Name))
				for _, ch := range p.Changes {
					fmt.Fprintf(f.w, "        %s\n", ch)
				}
			}
			fmt.Fprintln(f.w)
		}

		if len(cd.AddedConnectors) > 0 {
			fmt.Fprintf(f.w, "  %s\n", c.bold("Added connectors:"))
			for _, cn := range cd.AddedConnectors {
				fmt.Fprintf(f.w, "    %s (type=%s, state=%s, tasks=%d)\n",
					c.green("+ "+cn.Name), cn.Type, cn.State, cn.TaskCount)
			}
			fmt.Fprintln(f.w)
		}

		if len(cd.RemovedConnectors) > 0 {
			fmt.Fprintf(f.w, "  %s\n", c.bold("Removed connectors:"))
			for _, cn := range cd.RemovedConnectors {
				fmt.Fprintf(f.w, "    %s (type=%s, state=%s)\n",
					c.red("- "+cn.Name), cn.Type, cn.State)
			}
			fmt.Fprintln(f.w)
		}

		if len(cd.ChangedConnectors) > 0 {
			fmt.Fprintf(f.w, "  %s\n", c.bold("Changed connectors:"))
			for _, cn := range cd.ChangedConnectors {
				fmt.Fprintf(f.w, "    %s\n", c.yellow("~ "+cn.Name))
				for _, ch := range cn.Changes {
					fmt.Fprintf(f.w, "        %s\n", ch)
				}
				for _, tc := range cn.TaskChanges {
					fmt.Fprintf(f.w, "        %s\n", tc)
				}
			}
			fmt.Fprintln(f.w)
		}

		if len(cd.AddedSuspects) > 0 {
			fmt.Fprintf(f.w, "  %s\n", c.bold("New suspect tasks:"))
			for _, s := range cd.AddedSuspects {
				fmt.Fprintf(f.w, "    %s  score=%s\n",
					c.green(fmt.Sprintf("+ %s/task-%d", s.ConnectorName, s.TaskID)),
					c.scoreColor(s.Score)(fmt.Sprintf("%d", s.Score)))
			}
			fmt.Fprintln(f.w)
		}

		if len(cd.RemovedSuspects) > 0 {
			fmt.Fprintf(f.w, "  %s\n", c.bold("Removed suspect tasks:"))
			for _, s := range cd.RemovedSuspects {
				fmt.Fprintf(f.w, "    %s  score=%d\n",
					c.red(fmt.Sprintf("- %s/task-%d", s.ConnectorName, s.TaskID)), s.Score)
			}
			fmt.Fprintln(f.w)
		}

		if len(cd.ChangedSuspects) > 0 {
			fmt.Fprintf(f.w, "  %s\n", c.bold("Changed suspects:"))
			for _, s := range cd.ChangedSuspects {
				arrow := "="
				arrowColor := c.dim
				deltaStr := ""
				if s.ScoreDelta > 0 {
					arrow = "↑"
					arrowColor = c.boldRed
					deltaStr = fmt.Sprintf(" (+%d)", s.ScoreDelta)
				} else if s.ScoreDelta < 0 {
					arrow = "↓"
					arrowColor = c.boldGreen
					deltaStr = fmt.Sprintf(" (%d)", s.ScoreDelta)
				}
				fmt.Fprintf(f.w, "    %s  %d %s %d%s\n",
					c.yellow(fmt.Sprintf("~ %s/task-%d", s.ConnectorName, s.TaskID)),
					s.ScoreBefore, arrowColor(arrow), s.ScoreAfter, deltaStr)
				for _, ch := range s.Changes {
					fmt.Fprintf(f.w, "        %s\n", ch)
				}
			}
			fmt.Fprintln(f.w)
		}
	}

	if !hasChanges {
		fmt.Fprintf(f.w, "%s\n", c.boldGreen("No changes detected."))
	}

	return nil
}

// DoctorColorize returns a function that colorizes a status string.
// Returns nil when color is disabled (non-TTY or JSON mode).
func (f *Formatter) DoctorColorize() func(statusStr string, text string) string {
	if !f.c.enabled {
		return nil
	}
	return func(statusStr string, text string) string {
		switch statusStr {
		case "PASS":
			return f.c.boldGreen(text)
		case "WARN":
			return f.c.boldYellow(text)
		case "FAIL":
			return f.c.boldRed(text)
		case "SKIP":
			return f.c.dim(text)
		default:
			return text
		}
	}
}

func (f *Formatter) json(v interface{}) error {
	enc := json.NewEncoder(f.w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func fmtBytes(b int64) string {
	if b == 0 {
		return "N/A"
	}
	const gi = 1024 * 1024 * 1024
	const mi = 1024 * 1024
	const ki = 1024
	switch {
	case b >= gi:
		return fmt.Sprintf("%.1fGi", float64(b)/float64(gi))
	case b >= mi:
		return fmt.Sprintf("%.0fMi", float64(b)/float64(mi))
	case b >= ki:
		return fmt.Sprintf("%.0fKi", float64(b)/float64(ki))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func scoreBar(score int) string {
	filled := score / 5
	empty := 20 - filled
	if empty < 0 {
		empty = 0
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat(".", empty) + "]"
}

func taskSummary(tasks []models.TaskInfo) string {
	m := make(map[string]int)
	for _, t := range tasks {
		m[t.State]++
	}
	var parts []string
	for state, n := range m {
		parts = append(parts, fmt.Sprintf("%d %s", n, state))
	}
	return strings.Join(parts, ", ")
}

func shortClass(full string) string {
	if idx := strings.LastIndexByte(full, '.'); idx >= 0 {
		return full[idx+1:]
	}
	return full
}
