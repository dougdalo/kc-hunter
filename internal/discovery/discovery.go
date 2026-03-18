// Package discovery handles grouping of pods by Strimzi cluster,
// building worker-to-pod mappings, and URL extraction.
package discovery

import (
	"fmt"
	"sort"

	"github.com/dougdalo/kcdiag/pkg/models"
)

// GroupPodsByClusters groups pods by their strimzi.io/cluster label.
func GroupPodsByClusters(pods []models.PodInfo) map[string][]models.PodInfo {
	clusters := make(map[string][]models.PodInfo)
	for _, p := range pods {
		name := p.ClusterName
		if name == "" {
			name = "unknown"
		}
		clusters[name] = append(clusters[name], p)
	}
	return clusters
}

// BuildWorkers maps connectors/tasks to their worker pods.
// connectPort is needed because worker_id is typically <IP>:<port>.
func BuildWorkers(pods []models.PodInfo, connectors []models.ConnectorInfo, connectPort int) []models.WorkerInfo {
	podByWorker := make(map[string]*models.PodInfo)
	for i := range pods {
		p := &pods[i]
		if p.IP != "" {
			podByWorker[fmt.Sprintf("%s:%d", p.IP, connectPort)] = p
		}
		// Some Strimzi configs use pod hostname as worker_id
		podByWorker[fmt.Sprintf("%s:%d", p.Name, connectPort)] = p
	}

	workerTasks := make(map[string][]models.TaskInfo)
	workerConns := make(map[string]map[string]models.ConnectorInfo)

	for _, c := range connectors {
		for _, t := range c.Tasks {
			wid := t.WorkerID
			workerTasks[wid] = append(workerTasks[wid], t)
			if workerConns[wid] == nil {
				workerConns[wid] = make(map[string]models.ConnectorInfo)
			}
			workerConns[wid][c.Name] = c
		}
	}

	var workers []models.WorkerInfo
	for wid, tasks := range workerTasks {
		w := models.WorkerInfo{
			WorkerID:  wid,
			Pod:       podByWorker[wid],
			Tasks:     tasks,
			TaskCount: len(tasks),
		}
		for _, c := range workerConns[wid] {
			w.Connectors = append(w.Connectors, c)
		}
		workers = append(workers, w)
	}

	sort.Slice(workers, func(i, j int) bool {
		return workers[i].TaskCount > workers[j].TaskCount
	})

	return workers
}

// GetConnectURLs returns deduplicated Connect REST URLs from discovered pods.
func GetConnectURLs(pods []models.PodInfo) []string {
	seen := make(map[string]bool)
	var urls []string
	for _, p := range pods {
		if p.ConnectURL != "" && !seen[p.ConnectURL] {
			seen[p.ConnectURL] = true
			urls = append(urls, p.ConnectURL)
		}
	}
	return urls
}

// WorkerToPodMap builds worker_id -> pod name mapping using the configured port.
func WorkerToPodMap(pods []models.PodInfo, connectPort int) map[string]string {
	m := make(map[string]string, len(pods)*2)
	for _, p := range pods {
		if p.IP != "" {
			m[fmt.Sprintf("%s:%d", p.IP, connectPort)] = p.Name
		}
		m[fmt.Sprintf("%s:%d", p.Name, connectPort)] = p.Name
	}
	return m
}
