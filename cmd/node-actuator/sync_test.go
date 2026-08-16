/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

// stubSync swaps the flush for a recorder. Returns the call count and the ordering marker, because
// "did it sync" and "did it sync *before* the halt" are different questions and only the second one
// matters: a sync issued after reboot(2) never runs.
func stubSync(t *testing.T, order *[]string) *int {
	t.Helper()
	calls := 0
	previousSync := syncFilesystems
	previousReboot := rebootPoweroff
	syncFilesystems = func() {
		calls++
		*order = append(*order, "sync")
	}
	rebootPoweroff = func() error {
		*order = append(*order, "reboot")
		return nil
	}
	t.Cleanup(func() {
		syncFilesystems = previousSync
		rebootPoweroff = previousReboot
	})
	return &calls
}

// reboot(2) does not flush the page cache -- confirmed against kernel/reboot.c, where the
// LINUX_REBOOT_CMD_POWER_OFF branch reaches machine_power_off() without touching the filesystem,
// and stated outright in reboot(2)'s own man page: "If not preceded by a sync(2), data will be
// lost." Every dirty page the kernel has accepted but not written is gone otherwise.
func TestPoweroffSyncsBeforeHalting(t *testing.T) {
	var order []string
	calls := stubSync(t, &order)

	if err := runPoweroff(log.New(bytes.NewBuffer(nil), "", 0), nodeagent.ShutdownSignal{}); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}

	if *calls != 1 {
		t.Fatalf("expected exactly one sync before halting, got %d", *calls)
	}
	if len(order) != 2 || order[0] != "sync" || order[1] != "reboot" {
		t.Fatalf("sync must precede the halt; got %v. A sync issued after reboot(2) never runs.", order)
	}
}

// The skip exists because sync(2) blocks at the moment the runtime budget is tightest. It inverts
// its own outcome -- the nodes rushed to save time come back with the most to recover -- so it is
// never allowed to be silent.
func TestSkipSyncIsHonouredAndRecorded(t *testing.T) {
	var order []string
	calls := stubSync(t, &order)
	logs := bytes.NewBuffer(nil)

	payload := nodeagent.ShutdownSignal{ExecutionID: "exec-1", NodeName: "node-a", SkipSync: true}
	if err := runPoweroff(log.New(logs, "", 0), payload); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}

	if *calls != 0 {
		t.Errorf("sync ran %d time(s) despite SkipSync", *calls)
	}
	if len(order) != 1 || order[0] != "reboot" {
		t.Errorf("expected the halt alone, got %v", order)
	}
	recorded := logs.String()
	if !strings.Contains(recorded, "SKIPPING sync") {
		t.Errorf("the skip was not recorded; an unrecorded skip is indistinguishable from a sync that silently failed. Log was: %q", recorded)
	}
	if !strings.Contains(recorded, "node-a") {
		t.Errorf("the skip record does not name the node it applies to. Log was: %q", recorded)
	}
}

// Absent from the JSON means sync. A signal written by anything that predates the field, or by a
// writer that does not set it, must not be read as permission to drop the flush.
func TestSkipSyncDefaultsToSyncing(t *testing.T) {
	var order []string
	calls := stubSync(t, &order)

	var payload nodeagent.ShutdownSignal
	if err := runPoweroff(log.New(bytes.NewBuffer(nil), "", 0), payload); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("a signal that never mentions skipSync must still sync; got %d call(s)", *calls)
	}
}

// The sync duration is the only direct measurement of the handoff tail OD-27 reserves 20% of
// runtime for. If it stops being logged, that evidence stops arriving.
func TestSyncDurationIsLogged(t *testing.T) {
	var order []string
	stubSync(t, &order)
	logs := bytes.NewBuffer(nil)

	payload := nodeagent.ShutdownSignal{ExecutionID: "exec-2", NodeName: "node-b"}
	if err := runPoweroff(log.New(logs, "", 0), payload); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}
	recorded := logs.String()
	if !strings.Contains(recorded, "sync completed in") {
		t.Errorf("sync duration is not logged; OD-27 loses its only direct evidence. Log was: %q", recorded)
	}
	if !strings.Contains(recorded, "exec-2") {
		t.Errorf("the sync record is not correlatable to an execution. Log was: %q", recorded)
	}
}
