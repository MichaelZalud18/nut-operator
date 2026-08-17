package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

func traceLines(t *testing.T, logs *bytes.Buffer) []string {
	t.Helper()
	var gates []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "halt gate=") {
			gates = append(gates, line)
		}
	}
	return gates
}

func gateNames(lines []string) []string {
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		for _, field := range fields {
			if strings.HasPrefix(field, "gate=") {
				names = append(names, strings.TrimPrefix(field, "gate="))
			}
		}
	}
	return names
}

// A successful halt has to leave the full chain behind it, in order, ending at the syscall. That
// last line is the whole point: it is written before reboot(2) and cannot be written after, so its
// presence with the node still up is the host-PID-namespace finding, which nothing else can detect.
func TestHaltTraceRecordsEveryLinkThroughTheSyscall(t *testing.T) {
	var order []string
	stubSync(t, &order)

	logs := bytes.NewBuffer(nil)
	payload := nodeagent.ShutdownSignal{ExecutionID: "exec-trace", NodeName: "node-trace"}
	if err := runPoweroff(log.New(logs, "", 0), payload); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}

	lines := traceLines(t, logs)
	got := gateNames(lines)
	want := []string{gateSync, gateSync, gateCapabilityEffective, gateSyscallIssued}
	if len(got) != len(want) {
		t.Fatalf("expected gates %v, got %v\n%s", want, got, strings.Join(lines, "\n"))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("gate %d is %q, want %q\n%s", index, got[index], want[index], strings.Join(lines, "\n"))
		}
	}
	for _, line := range lines {
		if !strings.Contains(line, "node=node-trace") || !strings.Contains(line, "executionID=exec-trace") {
			t.Fatalf("every gate line must identify its node and execution: %q", line)
		}
	}
	if !strings.Contains(lines[len(lines)-1], "result=pass") {
		t.Fatalf("expected the syscall gate to pass, got %q", lines[len(lines)-1])
	}
}

// The sync gate has to be written before the wait, not after it. A trace that only records completed
// flushes cannot tell a sync that hung from a sync that was never reached, and those point at
// different halves of the system.
func TestHaltTraceRecordsASyncThatWasCutShort(t *testing.T) {
	previousSync := syncFilesystems
	previousReboot := rebootPoweroff
	previousRaise := raiseHaltCapability
	previousTimeout := syncTimeout
	release := make(chan struct{})
	syncFilesystems = func() { <-release }
	rebootPoweroff = func() error { return nil }
	raiseHaltCapability = func() error { return nil }
	syncTimeout = 20 * time.Millisecond
	t.Cleanup(func() {
		syncFilesystems = previousSync
		rebootPoweroff = previousReboot
		raiseHaltCapability = previousRaise
		syncTimeout = previousTimeout
		close(release)
	})

	logs := bytes.NewBuffer(nil)
	if err := runPoweroff(log.New(logs, "", 0), nodeagent.ShutdownSignal{NodeName: "node-wedged"}); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}

	lines := traceLines(t, logs)
	var started, cutShort bool
	for _, line := range lines {
		if strings.Contains(line, "gate="+gateSync) && strings.Contains(line, "started") {
			started = true
		}
		if strings.Contains(line, "gate="+gateSync) && strings.Contains(line, "result=fail") {
			cutShort = true
		}
	}
	if !started {
		t.Fatalf("expected the sync gate to be recorded before the wait\n%s", strings.Join(lines, "\n"))
	}
	if !cutShort {
		t.Fatalf("expected a wedged flush to be recorded as a failed gate\n%s", strings.Join(lines, "\n"))
	}
	if last := gateNames(lines); last[len(last)-1] != gateSyscallIssued {
		t.Fatalf("a wedged flush must still reach the syscall, got %v", last)
	}
}

// The skip is a pass, not a missing gate. A trace with no sync line at all reads as a flush that
// never ran because something broke, which is the opposite of what happened.
func TestHaltTraceRecordsTheSkipAsADecision(t *testing.T) {
	var order []string
	stubSync(t, &order)

	logs := bytes.NewBuffer(nil)
	payload := nodeagent.ShutdownSignal{NodeName: "node-skip", SkipSync: true}
	if err := runPoweroff(log.New(logs, "", 0), payload); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}

	lines := traceLines(t, logs)
	var recorded bool
	for _, line := range lines {
		if strings.Contains(line, "gate="+gateSync) {
			if !strings.Contains(line, "result=pass") || !strings.Contains(line, "skipped") {
				t.Fatalf("expected the skip recorded as a deliberate pass, got %q", line)
			}
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("expected a sync gate line for the skip\n%s", strings.Join(lines, "\n"))
	}
}

// The delivery channel is the first link and the only one that can be broken for months without
// anything asking it to do something, so it reports as soon as it is known. Logging it per tick
// would bury the burst the trace exists to produce, so it reports only on change after that.
func TestChannelTraceReportsOnceAndThenOnlyOnChange(t *testing.T) {
	logs := bytes.NewBuffer(nil)
	channel := channelTrace{trace: newGateTrace(log.New(logs, "", 0), "node-a", "")}

	channel.observe(nil, nil)
	if got := len(traceLines(t, logs)); got != 1 {
		t.Fatalf("expected the first observation to report, got %d lines", got)
	}
	channel.observe(nil, nil)
	channel.observe(nil, nil)
	if got := len(traceLines(t, logs)); got != 1 {
		t.Fatalf("expected an unchanged channel to stay quiet, got %d lines", got)
	}

	channel.observe(nil, []string{"/var/lib/power-agent/signals"})
	lines := traceLines(t, logs)
	if len(lines) != 2 {
		t.Fatalf("expected the channel breaking to report, got %d lines", len(lines))
	}
	broken := lines[1]
	if !strings.Contains(broken, "result=fail") {
		t.Fatalf("expected a failed gate, got %q", broken)
	}
	// The marker is what separates "no flow is running" from "this volume is not the operator's
	// Secret" (F-86), so naming it is the actionable half of the line.
	if !strings.Contains(broken, nodeagent.DeliveryChannelMarker) {
		t.Fatalf("expected the missing marker to be named, got %q", broken)
	}

	channel.observe(nil, nil)
	if got := len(traceLines(t, logs)); got != 3 {
		t.Fatalf("expected recovery to report, got %d lines", got)
	}
}

func TestChannelTraceSeparatesUnreadableFromUnprojected(t *testing.T) {
	logs := bytes.NewBuffer(nil)
	channel := channelTrace{trace: newGateTrace(log.New(logs, "", 0), "node-a", "")}

	channel.observe([]string{"/missing"}, []string{"/present-but-wrong"})

	lines := traceLines(t, logs)
	if len(lines) != 1 {
		t.Fatalf("expected one line, got %d", len(lines))
	}
	// Distinct on purpose: a directory that is not there and a directory that is there but is not
	// the operator's Secret are different faults with different fixes.
	if !strings.Contains(lines[0], "/missing") || !strings.Contains(lines[0], "/present-but-wrong") {
		t.Fatalf("expected both faults named, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "cannot be read") {
		t.Fatalf("expected the unreadable directory described as such, got %q", lines[0])
	}
}
