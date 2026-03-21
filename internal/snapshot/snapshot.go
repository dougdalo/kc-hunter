// Package snapshot provides save/load/diff for cluster state snapshots.
package snapshot

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

const snapshotVersion = "2"

// BuildSnapshot creates a Snapshot from cluster diagnostics.
func BuildSnapshot(diags []models.ClusterDiagnostic, meta *models.SnapshotMeta) models.Snapshot {
	snap := models.Snapshot{Version: snapshotVersion, Meta: meta}
	if len(diags) > 0 {
		snap.CollectedAt = diags[0].CollectedAt
	}

	for _, d := range diags {
		cs := models.ClusterSnapshot{
			ClusterName: d.ClusterName,
			Suspects:    d.Suspects,
		}

		// Capture pod snapshots.
		for _, p := range d.Pods {
			cs.Pods = append(cs.Pods, models.PodSnapshot{
				Name:          p.Name,
				NodeName:      p.NodeName,
				MemoryUsage:   p.MemoryUsage,
				MemoryLimit:   p.MemoryLimit,
				MemoryPercent: p.MemoryPercent,
				RestartCount:  p.RestartCount,
				Ready:         p.Ready,
			})
		}

		// Build connector snapshots from workers and suspects.
		connByName := make(map[string]*models.ConnectorSnapshot)

		// First pass: collect from workers (has full ConnectorInfo + tasks).
		for _, w := range d.Workers {
			for _, c := range w.Connectors {
				if _, ok := connByName[c.Name]; !ok {
					snap := &models.ConnectorSnapshot{
						Name:      c.Name,
						Type:      c.Type,
						State:     c.State,
						ClassName: c.ClassName,
						TaskCount: len(c.Tasks),
					}
					for _, t := range c.Tasks {
						snap.Tasks = append(snap.Tasks, models.TaskSnapshot{
							TaskID:   t.TaskID,
							State:    t.State,
							WorkerID: t.WorkerID,
						})
					}
					sort.Slice(snap.Tasks, func(i, j int) bool {
						return snap.Tasks[i].TaskID < snap.Tasks[j].TaskID
					})
					connByName[c.Name] = snap
				}
			}
		}

		// Second pass: fill in from suspects if worker data was missing.
		for _, s := range d.Suspects {
			if _, ok := connByName[s.ConnectorName]; !ok {
				connByName[s.ConnectorName] = &models.ConnectorSnapshot{
					Name:      s.ConnectorName,
					TaskCount: 0,
				}
			}
			cs := connByName[s.ConnectorName]
			if cs.TaskCount == 0 {
				cs.TaskCount++
			}
		}

		for _, c := range connByName {
			cs.Connectors = append(cs.Connectors, *c)
		}
		sort.Slice(cs.Connectors, func(i, j int) bool {
			return cs.Connectors[i].Name < cs.Connectors[j].Name
		})

		snap.Clusters = append(snap.Clusters, cs)
	}

	return snap
}

// Save writes a snapshot to a JSON file.
func Save(snap models.Snapshot, path string) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// supportedVersions lists snapshot format versions we can load.
var supportedVersions = map[string]bool{"1": true, "2": true}

// Load reads a snapshot from a JSON file.
func Load(path string) (models.Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return models.Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}
	var snap models.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return models.Snapshot{}, fmt.Errorf("parse snapshot: %w", err)
	}
	if snap.Version != "" && !supportedVersions[snap.Version] {
		return models.Snapshot{}, fmt.Errorf("unsupported snapshot version %q (supported: 1, 2)", snap.Version)
	}
	return snap, nil
}

// Diff compares two snapshots and returns a diff report.
func Diff(before, after models.Snapshot) models.DiffReport {
	report := models.DiffReport{
		BeforeTime: before.CollectedAt,
		AfterTime:  after.CollectedAt,
	}

	// Diff collection metadata.
	if md := diffMeta(before.Meta, after.Meta); md != nil {
		report.MetaDiff = md
	}

	// Index clusters by name.
	beforeClusters := make(map[string]models.ClusterSnapshot)
	for _, c := range before.Clusters {
		beforeClusters[c.ClusterName] = c
	}
	afterClusters := make(map[string]models.ClusterSnapshot)
	for _, c := range after.Clusters {
		afterClusters[c.ClusterName] = c
	}

	allClusters := make(map[string]bool)
	for name := range beforeClusters {
		allClusters[name] = true
	}
	for name := range afterClusters {
		allClusters[name] = true
	}

	for name := range allClusters {
		b := beforeClusters[name]
		a := afterClusters[name]
		cd := diffCluster(b, a)
		cd.ClusterName = name
		report.Clusters = append(report.Clusters, cd)
	}

	sort.Slice(report.Clusters, func(i, j int) bool {
		return report.Clusters[i].ClusterName < report.Clusters[j].ClusterName
	})

	report.Summary = buildSummary(&report)

	return report
}

func diffMeta(before, after *models.SnapshotMeta) *models.MetaDiff {
	if before == nil && after == nil {
		return nil
	}
	var changes []string

	bMeta := &models.SnapshotMeta{}
	aMeta := &models.SnapshotMeta{}
	if before != nil {
		bMeta = before
	}
	if after != nil {
		aMeta = after
	}

	if fmt.Sprint(bMeta.Namespaces) != fmt.Sprint(aMeta.Namespaces) {
		changes = append(changes, fmt.Sprintf("namespaces: %v -> %v", bMeta.Namespaces, aMeta.Namespaces))
	}
	if bMeta.Labels != aMeta.Labels {
		changes = append(changes, fmt.Sprintf("labels: %q -> %q", bMeta.Labels, aMeta.Labels))
	}
	if bMeta.Transport != aMeta.Transport {
		changes = append(changes, fmt.Sprintf("transport: %s -> %s", bMeta.Transport, aMeta.Transport))
	}
	if bMeta.MetricsSource != aMeta.MetricsSource {
		changes = append(changes, fmt.Sprintf("metricsSource: %s -> %s", bMeta.MetricsSource, aMeta.MetricsSource))
	}

	if len(changes) == 0 {
		return nil
	}
	return &models.MetaDiff{Changes: changes}
}

func diffCluster(before, after models.ClusterSnapshot) models.ClusterDiffReport {
	var d models.ClusterDiffReport

	// Diff pods.
	d.AddedPods, d.RemovedPods, d.ChangedPods = diffPods(before.Pods, after.Pods)

	// Diff connectors.
	bConn := make(map[string]models.ConnectorSnapshot)
	for _, c := range before.Connectors {
		bConn[c.Name] = c
	}
	aConn := make(map[string]models.ConnectorSnapshot)
	for _, c := range after.Connectors {
		aConn[c.Name] = c
	}

	for name, ac := range aConn {
		bc, existed := bConn[name]
		if !existed {
			d.AddedConnectors = append(d.AddedConnectors, ac)
			continue
		}
		changes, taskChanges := diffConnector(bc, ac)
		if len(changes) > 0 || len(taskChanges) > 0 {
			d.ChangedConnectors = append(d.ChangedConnectors, models.ConnectorChange{
				Name:        name,
				Changes:     changes,
				TaskChanges: taskChanges,
			})
		}
	}
	for name, bc := range bConn {
		if _, exists := aConn[name]; !exists {
			d.RemovedConnectors = append(d.RemovedConnectors, bc)
		}
	}

	// Diff suspects (keyed by connector/taskID).
	type suspectKey struct {
		connector string
		taskID    int
	}
	bSusp := make(map[suspectKey]models.SuspectReport)
	for _, s := range before.Suspects {
		bSusp[suspectKey{s.ConnectorName, s.TaskID}] = s
	}
	aSusp := make(map[suspectKey]models.SuspectReport)
	for _, s := range after.Suspects {
		aSusp[suspectKey{s.ConnectorName, s.TaskID}] = s
	}

	for key, as := range aSusp {
		bs, existed := bSusp[key]
		if !existed {
			d.AddedSuspects = append(d.AddedSuspects, as)
			continue
		}
		if changes := diffSuspect(bs, as); len(changes) > 0 {
			d.ChangedSuspects = append(d.ChangedSuspects, models.SuspectChange{
				ConnectorName: key.connector,
				TaskID:        key.taskID,
				ScoreBefore:   bs.Score,
				ScoreAfter:    as.Score,
				ScoreDelta:    as.Score - bs.Score,
				Changes:       changes,
			})
		}
	}
	for key, bs := range bSusp {
		if _, exists := aSusp[key]; !exists {
			d.RemovedSuspects = append(d.RemovedSuspects, bs)
		}
	}

	return d
}

func diffPods(before, after []models.PodSnapshot) (added, removed []models.PodSnapshot, changed []models.PodChange) {
	bPods := make(map[string]models.PodSnapshot)
	for _, p := range before {
		bPods[p.Name] = p
	}
	aPods := make(map[string]models.PodSnapshot)
	for _, p := range after {
		aPods[p.Name] = p
	}

	for name, ap := range aPods {
		bp, existed := bPods[name]
		if !existed {
			added = append(added, ap)
			continue
		}
		var changes []string
		if bp.RestartCount != ap.RestartCount {
			changes = append(changes, fmt.Sprintf("restarts: %d -> %d (+%d)",
				bp.RestartCount, ap.RestartCount, ap.RestartCount-bp.RestartCount))
		}
		if bp.Ready != ap.Ready {
			changes = append(changes, fmt.Sprintf("ready: %v -> %v", bp.Ready, ap.Ready))
		}
		if bp.NodeName != ap.NodeName {
			changes = append(changes, fmt.Sprintf("node: %s -> %s", bp.NodeName, ap.NodeName))
		}
		bPct := math.Round(bp.MemoryPercent*10) / 10
		aPct := math.Round(ap.MemoryPercent*10) / 10
		if bPct != aPct {
			changes = append(changes, fmt.Sprintf("memory: %.1f%% -> %.1f%%", bp.MemoryPercent, ap.MemoryPercent))
		}
		if len(changes) > 0 {
			changed = append(changed, models.PodChange{Name: name, Changes: changes})
		}
	}
	for name, bp := range bPods {
		if _, exists := aPods[name]; !exists {
			removed = append(removed, bp)
		}
	}
	return
}

func diffConnector(before, after models.ConnectorSnapshot) (changes, taskChanges []string) {
	if before.State != after.State {
		changes = append(changes, fmt.Sprintf("state: %s -> %s", before.State, after.State))
	}
	if before.TaskCount != after.TaskCount {
		changes = append(changes, fmt.Sprintf("tasks: %d -> %d", before.TaskCount, after.TaskCount))
	}
	if before.Type != after.Type && after.Type != "" {
		changes = append(changes, fmt.Sprintf("type: %s -> %s", before.Type, after.Type))
	}

	// Compare individual task states.
	bTasks := make(map[int]models.TaskSnapshot)
	for _, t := range before.Tasks {
		bTasks[t.TaskID] = t
	}
	aTasks := make(map[int]models.TaskSnapshot)
	for _, t := range after.Tasks {
		aTasks[t.TaskID] = t
	}

	for id, at := range aTasks {
		bt, existed := bTasks[id]
		if !existed {
			taskChanges = append(taskChanges, fmt.Sprintf("task-%d: added (state=%s, worker=%s)", id, at.State, at.WorkerID))
			continue
		}
		if bt.State != at.State {
			taskChanges = append(taskChanges, fmt.Sprintf("task-%d: state %s -> %s", id, bt.State, at.State))
		}
		if bt.WorkerID != at.WorkerID {
			taskChanges = append(taskChanges, fmt.Sprintf("task-%d: worker %s -> %s", id, bt.WorkerID, at.WorkerID))
		}
	}
	for id := range bTasks {
		if _, exists := aTasks[id]; !exists {
			taskChanges = append(taskChanges, fmt.Sprintf("task-%d: removed", id))
		}
	}

	return
}

func diffSuspect(before, after models.SuspectReport) []string {
	var changes []string

	if before.Score != after.Score {
		delta := after.Score - before.Score
		sign := "+"
		if delta < 0 {
			sign = ""
		}
		changes = append(changes, fmt.Sprintf("score: %d -> %d (%s%d)", before.Score, after.Score, sign, delta))
	}

	// Compare signals with values.
	type sigInfo struct {
		active bool
		value  string
		weight int
	}
	bSigs := make(map[string]sigInfo)
	for _, s := range before.Signals {
		bSigs[s.Name] = sigInfo{active: s.Active, value: s.Value, weight: s.Weight}
	}
	aSigs := make(map[string]sigInfo)
	for _, s := range after.Signals {
		aSigs[s.Name] = sigInfo{active: s.Active, value: s.Value, weight: s.Weight}
	}

	for name, as := range aSigs {
		bs, existed := bSigs[name]
		if !existed && as.active {
			changes = append(changes, fmt.Sprintf("signal fired: %s (weight=%d)", name, as.weight))
			continue
		}
		if !existed {
			continue
		}
		if !bs.active && as.active {
			changes = append(changes, fmt.Sprintf("signal fired: %s (weight=%d)", name, as.weight))
		} else if bs.active && !as.active {
			changes = append(changes, fmt.Sprintf("signal cleared: %s (weight=%d)", name, bs.weight))
		} else if bs.active && as.active && bs.value != as.value && as.value != "" {
			changes = append(changes, fmt.Sprintf("signal %s: %s -> %s", name, bs.value, as.value))
		}
	}
	for name, bs := range bSigs {
		if _, exists := aSigs[name]; !exists && bs.active {
			changes = append(changes, fmt.Sprintf("signal cleared: %s (weight=%d)", name, bs.weight))
		}
	}

	if before.WorkerID != after.WorkerID {
		changes = append(changes, fmt.Sprintf("worker: %s -> %s", before.WorkerID, after.WorkerID))
	}

	return changes
}

func buildSummary(report *models.DiffReport) *models.DiffSummary {
	s := &models.DiffSummary{}

	delta := report.AfterTime.Sub(report.BeforeTime)
	if delta < 0 {
		delta = -delta
	}
	hours := int(delta.Hours())
	minutes := int(delta.Minutes()) % 60
	if hours > 0 {
		s.TimeDelta = fmt.Sprintf("%dh%dm", hours, minutes)
	} else {
		s.TimeDelta = fmt.Sprintf("%dm", minutes)
	}

	if report.MetaDiff != nil {
		s.MetaChanged = true
	}

	maxDelta := 0
	for _, cd := range report.Clusters {
		s.ConnectorsAdded += len(cd.AddedConnectors)
		s.ConnectorsRemoved += len(cd.RemovedConnectors)
		s.ConnectorsChanged += len(cd.ChangedConnectors)
		s.PodsAdded += len(cd.AddedPods)
		s.PodsRemoved += len(cd.RemovedPods)

		for _, pc := range cd.ChangedPods {
			for _, ch := range pc.Changes {
				if len(ch) > 8 && ch[:8] == "restarts" {
					s.PodRestartChanges++
				}
			}
		}

		for _, cc := range cd.ChangedConnectors {
			s.TaskStateChanges += len(cc.TaskChanges)
		}

		for _, sc := range cd.ChangedSuspects {
			d := sc.ScoreAfter - sc.ScoreBefore
			if d > 0 {
				s.ScoreIncreases++
			} else if d < 0 {
				s.ScoreDecreases++
			}
			if abs(d) > maxDelta {
				maxDelta = abs(d)
			}
		}
	}
	s.MaxScoreDelta = maxDelta

	// Build headline.
	s.Headline = buildHeadline(s)

	return s
}

func buildHeadline(s *models.DiffSummary) string {
	totalChanges := s.ConnectorsAdded + s.ConnectorsRemoved + s.ConnectorsChanged +
		s.ScoreIncreases + s.ScoreDecreases + s.PodsAdded + s.PodsRemoved + s.PodRestartChanges

	if totalChanges == 0 {
		return "No significant changes detected"
	}

	if s.ScoreIncreases > 0 && s.MaxScoreDelta >= 20 {
		return fmt.Sprintf("Scores worsened significantly (max delta: +%d) — investigate new risk factors", s.MaxScoreDelta)
	}
	if s.ScoreDecreases > 0 && s.ScoreIncreases == 0 {
		return "Scores improved across the board — situation stabilizing"
	}
	if s.ConnectorsAdded > 0 || s.ConnectorsRemoved > 0 {
		return fmt.Sprintf("Topology changed: %d added, %d removed connectors", s.ConnectorsAdded, s.ConnectorsRemoved)
	}
	if s.PodRestartChanges > 0 {
		return fmt.Sprintf("%d pod(s) restarted — check for OOMKills or crashloops", s.PodRestartChanges)
	}
	if s.TaskStateChanges > 0 {
		return fmt.Sprintf("%d task state change(s) detected", s.TaskStateChanges)
	}
	return fmt.Sprintf("%d change(s) detected across clusters", totalChanges)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
