package k8s

import "testing"

func TestStrimziReplicationInfo_Mismatches(t *testing.T) {
	tests := []struct {
		name    string
		info    StrimziReplicationInfo
		wantN   int
		wantAll []string // expected FactorName values
	}{
		{
			name: "no mismatches when factors <= brokers",
			info: StrimziReplicationInfo{
				KafkaReplicas:                  3,
				ConfigStorageReplicationFactor: 3,
				OffsetStorageReplicationFactor: 2,
				StatusStorageReplicationFactor: 1,
			},
			wantN: 0,
		},
		{
			name: "all factors exceed single broker",
			info: StrimziReplicationInfo{
				KafkaReplicas:                  1,
				ConfigStorageReplicationFactor: 3,
				OffsetStorageReplicationFactor: 3,
				StatusStorageReplicationFactor: 3,
			},
			wantN:   3,
			wantAll: []string{"config.storage.replication.factor", "offset.storage.replication.factor", "status.storage.replication.factor"},
		},
		{
			name: "only one factor exceeds",
			info: StrimziReplicationInfo{
				KafkaReplicas:                  2,
				ConfigStorageReplicationFactor: 2,
				OffsetStorageReplicationFactor: 3,
				StatusStorageReplicationFactor: 1,
			},
			wantN:   1,
			wantAll: []string{"offset.storage.replication.factor"},
		},
		{
			name: "zero factors are ignored (not set)",
			info: StrimziReplicationInfo{
				KafkaReplicas:                  3,
				ConfigStorageReplicationFactor: 0,
				OffsetStorageReplicationFactor: 0,
				StatusStorageReplicationFactor: 0,
			},
			wantN: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.info.Mismatches()
			if len(got) != tt.wantN {
				t.Errorf("Mismatches() returned %d, want %d", len(got), tt.wantN)
			}
			for i, want := range tt.wantAll {
				if i < len(got) && got[i].FactorName != want {
					t.Errorf("mismatch[%d].FactorName=%q, want %q", i, got[i].FactorName, want)
				}
			}
		})
	}
}

func TestExtractReplicationFactor(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want int64
	}{
		{"int64", int64(3), 3},
		{"float64", float64(5), 5},
		{"string", "7", 7},
		{"missing key", nil, 0},
		{"bool (unexpected)", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]any{}
			if tt.val != nil {
				m["key"] = tt.val
			}
			got := extractReplicationFactor(m, "key")
			if got != tt.want {
				t.Errorf("extractReplicationFactor()=%d, want %d", got, tt.want)
			}
		})
	}
}
