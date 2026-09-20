package signalfixture

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

func TestCasesMatchRealActuatorInspection(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, guest := range []string{"hadron-node", "talos-node"} {
		for _, tc := range Invalid(guest, now) {
			t.Run(guest+"/"+tc.Name, func(t *testing.T) {
				data, err := json.Marshal(tc.Payload)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "signal.json")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if status := nodeagent.InspectSignal(path, 2*time.Minute, now, guest); status.Reason != tc.Reason {
					t.Fatalf("fixture reason=%s actual=%s", tc.Reason, status.Reason)
				}
			})
		}
	}
}

func TestPatchPreservesWrongNodePayloadAndEscapesKey(t *testing.T) {
	payload := Invalid("target", time.Now())[0].Payload
	data, err := SecretPatch("slot/~node", payload)
	if err != nil {
		t.Fatal(err)
	}
	var patch []struct{ Op, Path, Value string }
	if err := json.Unmarshal(data, &patch); err != nil {
		t.Fatal(err)
	}
	if len(patch) != 1 || patch[0].Path != "/data/slot~1~0node.json" || patch[0].Op != "add" {
		t.Fatalf("wrong patch: %s", data)
	}
	raw, err := base64.StdEncoding.DecodeString(patch[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded nodeagent.ShutdownSignal
	if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded, payload) {
		t.Fatalf("payload changed: %+v, %v", decoded, err)
	}
	if _, err := SecretPatch("", payload); err == nil {
		t.Fatal("empty slot accepted")
	}
}
