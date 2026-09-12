//go:build hadron

package hadron

import "testing"

func TestReadyNodeCondition(t *testing.T) {
	for _, tc := range []struct {
		output string
		ready  bool
	}{
		{`{"items":[{"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`, true},
		{`{"items":[{"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`, false},
		{`{"items":[{"status":{"conditions":[{"type":"Ready","status":"Unknown"}]}}]}`, false},
		{`{"items":[]}`, false},
		{`{"items":[{},{}]}`, false},
		{`{"items":[{}]}`, false},
		{"guest NotReady control-plane", false},
	} {
		if got := hasReadyNode(tc.output); got != tc.ready {
			t.Errorf("hasReadyNode(%s)=%v, want %v", tc.output, got, tc.ready)
		}
	}
}
