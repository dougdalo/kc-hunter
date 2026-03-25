// strimzi.go provides methods for querying Strimzi CRDs using the
// Kubernetes dynamic client. Since kc-hunter does not vendor Strimzi's
// Go types, all CRD access goes through unstructured objects.
package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// StrimziReplicationInfo holds the replication factor data extracted
// from Kafka and KafkaConnect CRDs in a single namespace.
type StrimziReplicationInfo struct {
	KafkaName     string
	KafkaReplicas int64

	ConnectName                    string
	ConfigStorageReplicationFactor int64
	OffsetStorageReplicationFactor int64
	StatusStorageReplicationFactor int64
}

// ReplicationMismatch describes a single replication factor that exceeds
// the number of available brokers.
type ReplicationMismatch struct {
	FactorName string // e.g. "config.storage.replication.factor"
	Value      int64
	Brokers    int64
}

// Mismatches returns all replication factors that exceed the broker count.
func (info *StrimziReplicationInfo) Mismatches() []ReplicationMismatch {
	factors := []struct {
		name  string
		value int64
	}{
		{"config.storage.replication.factor", info.ConfigStorageReplicationFactor},
		{"offset.storage.replication.factor", info.OffsetStorageReplicationFactor},
		{"status.storage.replication.factor", info.StatusStorageReplicationFactor},
	}

	var mismatches []ReplicationMismatch
	for _, f := range factors {
		if f.value > info.KafkaReplicas {
			mismatches = append(mismatches, ReplicationMismatch{
				FactorName: f.name,
				Value:      f.value,
				Brokers:    info.KafkaReplicas,
			})
		}
	}
	return mismatches
}

var (
	kafkaGVR = schema.GroupVersionResource{
		Group:    "kafka.strimzi.io",
		Version:  "v1beta2",
		Resource: "kafkas",
	}
	kafkaConnectGVR = schema.GroupVersionResource{
		Group:    "kafka.strimzi.io",
		Version:  "v1beta2",
		Resource: "kafkaconnects",
	}
)

// GetStrimziReplicationInfo fetches the Kafka and KafkaConnect CRDs from
// the given namespace and extracts replication factor configuration.
//
// Returns nil (no error) if no Kafka or KafkaConnect CR is found — the
// check simply doesn't apply.
func (c *Client) GetStrimziReplicationInfo(ctx context.Context, namespace string) ([]StrimziReplicationInfo, error) {
	dyn, err := dynamic.NewForConfig(c.restConfig)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	// List Kafka CRs.
	kafkaList, err := dyn.Resource(kafkaGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list Kafka CRs in namespace %q: %w", namespace, err)
	}
	if len(kafkaList.Items) == 0 {
		return nil, nil
	}

	// Build a map of Kafka name -> replicas for matching.
	kafkaReplicas := make(map[string]int64, len(kafkaList.Items))
	for _, kafka := range kafkaList.Items {
		replicas, found, err := unstructured.NestedInt64(kafka.Object, "spec", "kafka", "replicas")
		if err != nil || !found {
			continue
		}
		kafkaReplicas[kafka.GetName()] = replicas
	}

	// List KafkaConnect CRs.
	connectList, err := dyn.Resource(kafkaConnectGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list KafkaConnect CRs in namespace %q: %w", namespace, err)
	}
	if len(connectList.Items) == 0 {
		return nil, nil
	}

	var results []StrimziReplicationInfo
	for _, kc := range connectList.Items {
		// Try to match the KafkaConnect to a Kafka cluster.
		// Convention: KafkaConnect.metadata.labels["strimzi.io/cluster"] points
		// to the Kafka CR name. Fall back to first Kafka if not labeled.
		clusterRef := kc.GetLabels()["strimzi.io/cluster"]

		var brokerCount int64
		var kafkaName string
		if replicas, ok := kafkaReplicas[clusterRef]; ok {
			brokerCount = replicas
			kafkaName = clusterRef
		} else if len(kafkaReplicas) == 1 {
			// Single Kafka cluster in namespace — use it.
			for name, replicas := range kafkaReplicas {
				kafkaName = name
				brokerCount = replicas
			}
		} else {
			// Cannot determine which Kafka cluster this Connect belongs to.
			continue
		}

		info := StrimziReplicationInfo{
			KafkaName:     kafkaName,
			KafkaReplicas: brokerCount,
			ConnectName:   kc.GetName(),
		}

		// Extract replication factors from spec.config.
		// Strimzi stores these as untyped values — they can be int64, float64,
		// or even string in the CR. We handle all cases.
		configMap, _, _ := unstructured.NestedMap(kc.Object, "spec", "config")
		if configMap != nil {
			info.ConfigStorageReplicationFactor = extractReplicationFactor(configMap, "config.storage.replication.factor")
			info.OffsetStorageReplicationFactor = extractReplicationFactor(configMap, "offset.storage.replication.factor")
			info.StatusStorageReplicationFactor = extractReplicationFactor(configMap, "status.storage.replication.factor")
		}

		results = append(results, info)
	}

	return results, nil
}

// extractReplicationFactor safely extracts a replication factor from the
// Strimzi config map. The value can be int64, float64, or string in the
// unstructured representation.
func extractReplicationFactor(configMap map[string]any, key string) int64 {
	val, ok := configMap[key]
	if !ok {
		return 0
	}

	switch v := val.(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		var n int64
		fmt.Sscanf(v, "%d", &n)
		return n
	default:
		return 0
	}
}
