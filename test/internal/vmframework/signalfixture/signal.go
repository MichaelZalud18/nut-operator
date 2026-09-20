// Package signalfixture supplies deterministic payload fixtures and Secret patch
// encoding. It never equates controller revocation with actuator rejection.
package signalfixture

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

type Case struct {
	Name, Reason string
	Payload      nodeagent.ShutdownSignal
}

// Invalid reproduces the five existing VM signal cases with a caller-supplied
// clock. Payload values are independent so mutating one cannot alter another.
func Invalid(node string, now time.Time) []Case {
	base := nodeagent.ShutdownSignal{NodeName: node, PlanConfigHash: "test-hash", ShutdownFlow: "test-flow", Timestamp: now.UTC().Format(time.RFC3339Nano)}
	cases := []Case{
		{Name: "wrong-node", Reason: "SignalWrongNode", Payload: base},
		{Name: "stale", Reason: "SignalStale", Payload: base},
		{Name: "future", Reason: "SignalFromFuture", Payload: base},
		{Name: "malformed-timestamp", Reason: "SignalInvalidTimestamp", Payload: base},
		{Name: "missing-fields", Reason: "SignalMissingRequiredFields", Payload: base},
	}
	for i := range cases {
		cases[i].Payload.ExecutionID = "exec-" + cases[i].Name
	}
	cases[0].Payload.NodeName += "-not-this-one"
	cases[1].Payload.Timestamp = now.Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	cases[2].Payload.Timestamp = now.Add(10 * time.Minute).UTC().Format(time.RFC3339Nano)
	cases[3].Payload.Timestamp = "not-a-timestamp"
	cases[4].Payload.PlanConfigHash, cases[4].Payload.ShutdownFlow = "", ""
	return cases
}

// SecretPatch patches the selected node's slot; the payload may intentionally
// name a different node. JSON Pointer escaping keeps the slot a single map key.
func SecretPatch(node string, payload nodeagent.ShutdownSignal) ([]byte, error) {
	if node == "" {
		return nil, fmt.Errorf("signal slot node is required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	key := strings.NewReplacer("~", "~0", "/", "~1").Replace(node + ".json")
	return json.Marshal([]struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}{{"add", "/data/" + key, base64.StdEncoding.EncodeToString(data)}})
}
