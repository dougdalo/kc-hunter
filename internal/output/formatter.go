// Package output handles rendering diagnostic results as table or JSON.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/dougdalo/kcdiag/pkg/models"
)

// Formatter renders diagnostic data.
type Formatter struct {
	format string
	w      io.Writer
}

func NewFormatter(format string, w io.Writer) *Formatter {
	return &Formatter{format: format, w: w}
}

// PrintPods renders pod resource usage.
func (f *Formatter) PrintPods(pods []models.PodInfo) error {
	if f.format == "json" {
		return f.json(pods)
	}

	tw := tabwriter.NewWriter(f.w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "POD\tCLUSTER\tNODE\tMEM USED\tMEM LIMIT\tMEM%\tCPU(m)\tREADY\tRESTARTS")
	fmt.Fprintln(tw, "---\t-------\t----\t--------\t---------\t----\t------\t-----\t--------")
	for _, p := range pods {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%.1f%%\t%d\t%v\t%d\n",
			p.Name, p.ClusterName, p.NodeName,
			fmtBytes(p.MemoryUsage), fmtBytes(p.MemoryLimit),
			p.MemoryPercent, p.CPUUsage, p.Ready, p.RestartCount)
	}
	return tw.Flush()
}

// PrintWorkers renders worker-to-task mapping.
func (f *Formatter) PrintWorkers(workers []models.WorkerInfo) error {
	if f.format == "json" {
		return f.json(workers)
	}

	for _, w := range workers {
		fmt.Fprintf(f.w, "\n--- Worker: %s", w.WorkerID)
		if w.Pod != nil {
			fmt.Fprintf(f.w, "  (pod: %s, mem: %s / %s = %.1f%%)",
				w.Pod.Name, fmtBytes(w.Pod.MemoryUsage), fmtBytes(w.Pod.MemoryLimit), w.Pod.MemoryPercent)
		}
		fmt.Fprintf(f.w, " ---\nTasks: %d\n\n", w.TaskCount)

		tw := tabwriter.NewWriter(f.w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  CONNECTOR\tTASK\tSTATE\tTYPE")
		fmt.Fprintln(tw, "  ---------\t----\t-----\t----")
		for _, t := range w.Tasks {
			cType := ""
			for _, c := range w.Connectors {
				if c.Name == t.ConnectorName {
					cType = c.Type
					break
				}
			}
			fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\n", t.ConnectorName, t.TaskID, t.State, cType)
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
	fmt.Fprintln(tw, "CONNECTOR\tTYPE\tSTATE\tTASKS\tWORKER\tCLASS")
	fmt.Fprintln(tw, "---------\t----\t-----\t-----\t------\t-----")
	for _, c := range connectors {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			c.Name, c.Type, c.State, taskSummary(c.Tasks), c.WorkerID, shortClass(c.ClassName))
	}
	return tw.Flush()
}

// PrintSuspects renders the ranked suspect report.
func (f *Formatter) PrintSuspects(diag models.ClusterDiagnostic, topN int) error {
	if f.format == "json" {
		return f.json(diag)
	}

	fmt.Fprintf(f.w, "\n========================================\n")
	fmt.Fprintf(f.w, " SUSPECT REPORT: %s\n", diag.ClusterName)
	fmt.Fprintf(f.w, "========================================\n\n")

	if p := diag.HottestPod; p != nil {
		fmt.Fprintf(f.w, "Hottest pod: %s\n", p.Name)
		fmt.Fprintf(f.w, "  Memory: %s / %s (%.1f%%)\n", fmtBytes(p.MemoryUsage), fmtBytes(p.MemoryLimit), p.MemoryPercent)
		fmt.Fprintf(f.w, "  Node:   %s\n\n", p.NodeName)
	}

	suspects := diag.Suspects
	if topN > 0 && len(suspects) > topN {
		suspects = suspects[:topN]
	}

	for i, s := range suspects {
		bar := scoreBar(s.Score)
		fmt.Fprintf(f.w, "  %d. %s / task-%d\n", i+1, s.ConnectorName, s.TaskID)
		fmt.Fprintf(f.w, "     Worker: %s (pod: %s)\n", s.WorkerID, s.PodName)
		fmt.Fprintf(f.w, "     Score:  %s %d/100\n", bar, s.Score)

		if len(s.Reasons) > 0 {
			fmt.Fprintf(f.w, "     Reasons:\n")
			for _, r := range s.Reasons {
				fmt.Fprintf(f.w, "       - %s\n", r)
			}
		}

		if s.Recommendation != "" {
			fmt.Fprintf(f.w, "     Recommendation: %s\n", s.Recommendation)
		}
		fmt.Fprintln(f.w)
	}

	// Summary
	failedCount := 0
	for _, s := range diag.Suspects {
		for _, sig := range s.Signals {
			if sig.Name == "task_failed" && sig.Active {
				failedCount++
			}
		}
	}
	fmt.Fprintf(f.w, "----------------------------------------\n")
	fmt.Fprintf(f.w, "Total tasks analyzed: %d\n", len(diag.Suspects))
	fmt.Fprintf(f.w, "Failed tasks: %d\n", failedCount)
	fmt.Fprintf(f.w, "Showing top %d suspects\n", len(suspects))

	return nil
}

// PrintConnectorDetail renders detailed info for a single connector.
func (f *Formatter) PrintConnectorDetail(c *models.ConnectorInfo) error {
	if f.format == "json" {
		return f.json(c)
	}

	fmt.Fprintf(f.w, "\nConnector: %s\n", c.Name)
	fmt.Fprintf(f.w, "Type:      %s\n", c.Type)
	fmt.Fprintf(f.w, "State:     %s\n", c.State)
	fmt.Fprintf(f.w, "Worker:    %s\n", c.WorkerID)
	fmt.Fprintf(f.w, "Class:     %s\n", c.ClassName)
	fmt.Fprintf(f.w, "Tasks:     %d\n\n", len(c.Tasks))

	for _, t := range c.Tasks {
		fmt.Fprintf(f.w, "  Task %d: state=%s worker=%s\n", t.TaskID, t.State, t.WorkerID)
		if t.Trace != "" {
			trace := t.Trace
			if len(trace) > 300 {
				trace = trace[:300] + "..."
			}
			fmt.Fprintf(f.w, "    Error: %s\n", trace)
		}
	}
	return nil
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
