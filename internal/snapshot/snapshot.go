// Package snapshot provides save/load/diff for cluster state snapshots.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/dougdalo/kc-hunter/pkg/models"
)

const snapshotVersion = "1"

// BuildSnapshot creates a Snapshot from cluster diagnostics.
func BuildSnapshot(diags []models.ClusterDiagnostic) models.Snapshot {
	snap := models.Snapshot{Version: snapshotVersion}
	if len(diags) > 0 {
		snap.CollectedAt = diags[0].CollectedAt
	}

	for _, d := range diags {
		cs := models.ClusterSnapshot{
			ClusterName: d.ClusterName,
			Suspects:    d.Suspects,
		}

		// Deduplicate connectors from suspects — each connector may have
		// multiple tasks, but we only want one entry per connector name.
		seen := make(map[string]bool)
		for _, s := range d.Suspects {
			if seen[s.ConnectorName] {
				continue
			}
			seen[s.ConnectorName] = true
		}

		// Build connector snapshots from the suspect list. Workers carry
		// the full ConnectorInfo, but suspects are always available and
		// contain the fields we need for diffing.
		connByName := make(map[string]*models.ConnectorSnapshot)
		for _, s := range d.Suspects {
			if _, ok := connByName[s.ConnectorName]; !ok {
				connByName[s.ConnectorName] = &models.ConnectorSnapshot{
					Name:      s.ConnectorName,
					TaskCount: 0,
				}
			}
			connByName[s.ConnectorName].TaskCount++
		}
		// Enrich from workers if available.
		for _, w := range d.Workers {
			for _, c := range w.Connectors {
				if cs, ok := connByName[c.Name]; ok {
					cs.Type = c.Type
					cs.State = c.State
					cs.ClassName = c.ClassName
				}
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
	return snap, nil
}

// Diff compares two snapshots and returns a diff report.
func Diff(before, after models.Snapshot) models.DiffReport {
	report := models.DiffReport{
		BeforeTime: before.CollectedAt,
		AfterTime:  after.CollectedAt,
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

	// All cluster names.
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

	return report
}

func diffCluster(before, after models.ClusterSnapshot) models.ClusterDiffReport {
	var d models.ClusterDiffReport

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
		if changes := diffConnector(bc, ac); len(changes) > 0 {
			d.ChangedConnectors = append(d.ChangedConnectors, models.ConnectorChange{
				Name:    name,
				Changes: changes,
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

func diffConnector(before, after models.ConnectorSnapshot) []string {
	var changes []string
	if before.State != after.State {
		changes = append(changes, fmt.Sprintf("state: %s -> %s", before.State, after.State))
	}
	if before.TaskCount != after.TaskCount {
		changes = append(changes, fmt.Sprintf("tasks: %d -> %d", before.TaskCount, after.TaskCount))
	}
	if before.Type != after.Type && after.Type != "" {
		changes = append(changes, fmt.Sprintf("type: %s -> %s", before.Type, after.Type))
	}
	return changes
}

func diffSuspect(before, after models.SuspectReport) []string {
	var changes []string

	if before.Score != after.Score {
		changes = append(changes, fmt.Sprintf("score: %d -> %d", before.Score, after.Score))
	}

	// Compare active signals.
	bSigs := make(map[string]bool)
	for _, s := range before.Signals {
		if s.Active {
			bSigs[s.Name] = true
		}
	}
	aSigs := make(map[string]bool)
	for _, s := range after.Signals {
		if s.Active {
			aSigs[s.Name] = true
		}
	}

	for name := range aSigs {
		if !bSigs[name] {
			changes = append(changes, fmt.Sprintf("signal added: %s", name))
		}
	}
	for name := range bSigs {
		if !aSigs[name] {
			changes = append(changes, fmt.Sprintf("signal removed: %s", name))
		}
	}

	return changes
}
