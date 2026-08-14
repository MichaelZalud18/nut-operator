// Command power-notify-writer records upsmon notifications to a file (F-68).
//
// upsmon's notification surface was entirely unconfigured, which did not mean "quiet": with no
// NOTIFYCMD set, upsmon falls back to `wall`, which the operand image does not ship, so every
// notification produced "Warning: no custom notification command defined" followed by
// "sh: wall: not found". The events were failing, not unused.
//
// This is the receiving end. upsmon invokes it as `power-notify-writer <message>` with NOTIFYTYPE
// and UPSNAME in the environment, and it appends the event to a JSON file. It interprets nothing:
// COMMOK and COMMBAD are recorded exactly as upsmon dispatched them, and what they mean for
// readiness or for a flow is the reader's decision (F-65 is the first such reader).
//
// It must never fail loudly. upsmon runs NOTIFYCMD synchronously on its own polling thread, so a
// writer that blocks or crashes degrades the thing it is reporting on -- during exactly the event
// it exists to report.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var version = "dev"

// maxRecordedEvents bounds the file.
//
// The file is rewritten in full on every event, so this is both a history depth and a write size.
// Enough to cover an outage's worth of transitions, small enough that the write stays trivial next
// to a UPS polling interval.
const maxRecordedEvents = 50

type notifyEvent struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	UPS       string `json:"ups,omitempty"`
	Message   string `json:"message,omitempty"`
}

type notifyState struct {
	// Events is oldest-first, so appending is the natural operation and the newest is last.
	Events []notifyEvent `json:"events"`
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println(version)
			return
		case "-h", "--help", "help":
			fmt.Println("Usage: power-notify-writer [--check] <message>")
			fmt.Println("Records a upsmon NOTIFYCMD dispatch. NOTIFYTYPE and UPSNAME are read from the environment.")
			fmt.Println("--check exits non-zero when upsmon last reported losing contact with a UPS.")
			return
		case "--check", "check":
			// The readiness half of F-65, and the reason the notification surface is worth having.
			//
			// upsc can prove a server is reachable and serving a UPS, and cannot prove upsmon holds
			// a session with it -- upsc does not read upsmon.conf, so it exercises neither the
			// MONITOR credentials nor the TLS posture. That was the gap F-40 fell into, where
			// upsmon failed with an SSL error against a server upsc reached fine.
			//
			// COMMOK and COMMBAD come from upsmon's own session, so they answer the question upsc
			// cannot. This only reads what upsmon already said.
			if err := checkCommunication(env("POWER_NOTIFY_STATE_PATH", "/run/power-agent/notify.json")); err != nil {
				fmt.Fprintf(os.Stderr, "power-notify-writer: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	path := env("POWER_NOTIFY_STATE_PATH", "/run/power-agent/notify.json")
	event := notifyEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Type:      env("NOTIFYTYPE", "UNKNOWN"),
		UPS:       env("UPSNAME", ""),
		Message:   strings.Join(os.Args[1:], " "),
	}

	if err := record(path, event); err != nil {
		// Reported and swallowed. Exiting non-zero would put an error in upsmon's log on every
		// event without changing anything a reader can act on, and the notification itself has
		// already been dispatched to SYSLOG by the NOTIFYFLAG beside EXEC.
		fmt.Fprintf(os.Stderr, "power-notify-writer: %v\n", err)
	}
}

func record(path string, event notifyEvent) error {
	state := notifyState{}
	if raw, err := os.ReadFile(path); err == nil {
		// A corrupt or truncated file is discarded rather than propagated. Losing history is
		// tolerable; refusing to record the event that is happening now is not.
		_ = json.Unmarshal(raw, &state)
	}

	state.Events = append(state.Events, event)
	if len(state.Events) > maxRecordedEvents {
		state.Events = state.Events[len(state.Events)-maxRecordedEvents:]
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode notify state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create notify state directory: %w", err)
	}

	// Written and renamed so a reader polling this file never sees a partial record.
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write notify state: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace notify state: %w", err)
	}
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// commEventOrder is the set of events that describe session health, and nothing else.
//
// ONBATT is not here on purpose: a node on battery has a perfectly good session with a UPS that is
// telling it something important. Reporting NotReady for it would take the agent out of service at
// the one moment it matters, which is the F-35 mistake in a different place.
var commEventOrder = map[string]bool{
	"COMMOK":  true,
	"COMMBAD": false,
	"NOCOMM":  false,
}

// checkCommunication fails when upsmon's most recent word on a UPS was that it lost contact.
//
// Silence is not failure. A fresh agent that has never dispatched a communication event has nothing
// to report, and treating that as unhealthy would make every rollout fail readiness until the first
// event happened to fire.
func checkCommunication(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read notify state: %w", err)
	}
	var state notifyState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("parse notify state: %w", err)
	}

	// Last word wins, per UPS. An earlier COMMBAD followed by COMMOK is a recovered link.
	latest := map[string]string{}
	for _, event := range state.Events {
		if _, communication := commEventOrder[event.Type]; communication {
			latest[event.UPS] = event.Type
		}
	}
	for ups, event := range latest {
		if !commEventOrder[event] {
			return fmt.Errorf("upsmon last reported %s for UPS %q", event, ups)
		}
	}
	return nil
}
