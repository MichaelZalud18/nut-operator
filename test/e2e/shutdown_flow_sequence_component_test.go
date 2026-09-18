//go:build e2e

package e2e

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// These assertions constrain fixture content, not NUT's parser or Kubernetes
// projection latency. The real Kind test remains the adoption/telemetry proof.
func TestLogicalFlowSequenceTiming(t *testing.T) {
	for _, tc := range []struct {
		name   string
		outage bool
		want   []string
	}{
		{"preoutage bounded Online loop", false, []string{
			"ups.status: OL", "battery.charge: 100", "battery.runtime: 3600", "TIMER 5",
		}},
		{"outage Online then sustained OnBattery", true, []string{
			"ups.status: OL", "battery.charge: 100", "battery.runtime: 3600", "TIMER 5",
			"ups.status: OB", "battery.charge: 40", "battery.runtime: 1800", "TIMER 900",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var list corev1.List
			decodeLogicalFixture(t, logicalFlowSequence(tc.outage), &list)
			if len(list.Items) != 1 {
				t.Fatal("sequence update must contain only its ConfigMap")
			}
			var cm corev1.ConfigMap
			if err := json.Unmarshal(list.Items[0].Raw, &cm); err != nil {
				t.Fatal(err)
			}
			if cm.Kind != "ConfigMap" || cm.Name != "test2-sequence" || cm.Namespace != flowOperandNamespace || len(cm.Data) != 1 {
				t.Fatal("sequence update must preserve the same ConfigMap and single data key")
			}
			content, ok := cm.Data["sequence.seq"]
			if !ok {
				t.Fatal("sequence update changed its projected key")
			}
			var steps []string
			for _, line := range strings.Split(content, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "ups.status:") || strings.HasPrefix(line, "battery.") || strings.HasPrefix(line, "TIMER") {
					steps = append(steps, line)
				}
			}
			if !reflect.DeepEqual(steps, tc.want) {
				t.Fatalf("sequence steps = %q, want %q", steps, tc.want)
			}
			if !strings.HasSuffix(content, tc.want[len(tc.want)-1]+"\n") {
				t.Fatal("sequence must end after its final timer so dummy-loop reaches EOF")
			}
		})
	}
}
