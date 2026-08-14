package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeState(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notify.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	return path
}

// F-65: this is the half upsc cannot reach. COMMBAD comes from upsmon's own session, which used the
// MONITOR credentials and the TLS posture upsc never sees.
func TestCheckFailsWhenUpsmonLastLostContact(t *testing.T) {
	path := writeState(t, `{"events":[{"type":"COMMOK","ups":"ups-a"},{"type":"COMMBAD","ups":"ups-a"}]}`)

	err := checkCommunication(path)
	if err == nil {
		t.Fatal("a UPS whose last communication event was COMMBAD must fail readiness")
	}
	if !strings.Contains(err.Error(), "ups-a") {
		t.Errorf("the message must name the UPS, got: %v", err)
	}
}

// A recovered link is not a failure, or an agent would stay NotReady forever after one blip.
func TestCheckPassesAfterCommunicationRecovers(t *testing.T) {
	path := writeState(t, `{"events":[{"type":"COMMBAD","ups":"ups-a"},{"type":"COMMOK","ups":"ups-a"}]}`)

	if err := checkCommunication(path); err != nil {
		t.Fatalf("a recovered link must pass: %v", err)
	}
}

// Silence is not failure. Treating a fresh agent as unhealthy would fail readiness on every rollout
// until some event happened to fire.
func TestCheckPassesWhenNothingHasBeenReported(t *testing.T) {
	if err := checkCommunication(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("an agent that has reported nothing must pass: %v", err)
	}
}

// A node on battery has a perfectly good session with a UPS telling it something important.
// Reporting NotReady there would take the agent out of service at the moment it matters most.
func TestCheckIgnoresPowerEventsAndOnlyJudgesCommunication(t *testing.T) {
	path := writeState(t, `{"events":[{"type":"COMMOK","ups":"ups-a"},{"type":"ONBATT","ups":"ups-a"},{"type":"LOWBATT","ups":"ups-a"}]}`)

	if err := checkCommunication(path); err != nil {
		t.Fatalf("power events must not affect readiness: %v", err)
	}
}

// Each UPS is judged on its own last word, so one dead link is not hidden by another healthy one.
func TestCheckFailsWhenAnyMonitoredUPSLostContact(t *testing.T) {
	path := writeState(t, `{"events":[{"type":"COMMOK","ups":"ups-a"},{"type":"NOCOMM","ups":"ups-b"}]}`)

	if err := checkCommunication(path); err == nil {
		t.Fatal("one UPS out of contact must fail readiness even when another is fine")
	}
}
