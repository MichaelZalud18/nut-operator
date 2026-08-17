package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

// The halt path is a chain of links, each of which can break on its own and none of which reports
// anything on its own. A node that stays up after a shutdown signal is the same observation whether
// the operator never wrote the Secret, kubelet never projected it, the actuator rejected the payload,
// the capability was missing, or reboot(2) itself returned an error -- and the last of those is
// indistinguishable from success when the container is not really in the host PID namespace.
//
// So each link announces itself. `make verify-actuation` exists to break exactly one of them on
// purpose and find out which; without a per-link trace, the run's whole output is a machine that is
// either dark or not.
//
// This is not a log level. Turning up verbosity globally would bury the one interesting burst under
// the polling loop, which deliberately says nothing on a normal tick -- SignalMissing is the common
// case and logging it would be a line every five seconds forever. These gates are scoped to the path
// that halts the machine, and that path runs at most once in a container's life: the trace is a
// handful of lines, once, ever. There is nothing to gate them behind and nothing to switch on at the
// moment somebody needs them, which is the moment nobody can reach the node to change a setting.
const (
	// gateCapabilityPermitted is checked at startup, not at halt time. Arming is where a missing
	// CAP_SYS_BOOT is still cheap to notice.
	gateCapabilityPermitted = "CapabilityPermitted"
	// gateSignalChannel is whether the operator's projected Secret is actually mounted here. Logged
	// on transition rather than per tick, because it is a standing condition and not an event.
	gateSignalChannel = "SignalChannel"
	// gateSignalAccepted covers read, parse, required fields, node binding, and TTL -- one gate
	// because InspectSignal answers them with one reason code, which is the thing worth printing.
	gateSignalAccepted = "SignalAccepted"
	// gateFlowBinding is the agent's own spec.shutdownFlowRef check (F-55), separate from
	// InspectSignal because a signal can be perfectly valid and still belong to another flow.
	gateFlowBinding = "FlowBinding"
	// gateModeAuthorized is the actuator's own POWER_AGENT_MODE, read from this process's
	// environment and never from the signal (F-56).
	gateModeAuthorized = "ModeAuthorized"
	// gateSync is the flush, including the case where the executor asked to skip it and the case
	// where a hung mount outlasted the bound.
	gateSync = "Sync"
	// gateCapabilityEffective is the permitted-to-effective raise. It is the last thing that can
	// fail with a diagnosable error.
	gateCapabilityEffective = "CapabilityEffective"
	// gateSyscallIssued is written before reboot(2) and cannot be written after it: on a working
	// actuate path the process does not survive to log anything else. Its presence with nothing
	// following it, and the node still up, is the host-PID-namespace finding -- from a non-initial
	// namespace the syscall returns success and does nothing.
	gateSyscallIssued = "SyscallIssued"
)

// gateTrace writes the per-link record of one halt attempt.
type gateTrace struct {
	logger      *log.Logger
	node        string
	executionID string
}

func newGateTrace(logger *log.Logger, node, executionID string) gateTrace {
	return gateTrace{logger: logger, node: node, executionID: executionID}
}

// pass records that a link held.
func (t gateTrace) pass(gate, detail string) {
	t.write(gate, "pass", detail)
}

// fail records that a link broke, and is the line the verification run exists to produce.
func (t gateTrace) fail(gate, detail string) {
	t.write(gate, "fail", detail)
}

func (t gateTrace) write(gate, result, detail string) {
	if t.logger == nil {
		return
	}
	// One shape for every gate so the whole trace greps as a unit: `grep 'halt gate='`. The fields
	// are ordered outcome-first because the failing line is what someone is scanning for.
	var builder strings.Builder
	fmt.Fprintf(&builder, "halt gate=%s result=%s", gate, result)
	if detail != "" {
		fmt.Fprintf(&builder, " detail=%q", detail)
	}
	if t.node != "" {
		fmt.Fprintf(&builder, " node=%s", t.node)
	}
	if t.executionID != "" {
		fmt.Fprintf(&builder, " executionID=%s", t.executionID)
	}
	t.logger.Print(builder.String())
}

// channelTrace reports the delivery channel's state on transition.
//
// A standing condition rather than an event: the directory either carries the operator's Secret or
// it does not, and it can sit either way for months. Logged only when it changes, because the watch
// loop evaluates it every tick and a line per tick would make the trace unreadable at the moment it
// matters. The first evaluation always logs, so a container that starts with no channel says so
// immediately rather than waiting for a change that may never come.
type channelTrace struct {
	trace   gateTrace
	known   bool
	healthy bool
}

func (c *channelTrace) observe(unreadable, unprojected []string) {
	healthy := len(unreadable) == 0 && len(unprojected) == 0
	if c.known && healthy == c.healthy {
		return
	}
	c.known = true
	c.healthy = healthy
	if healthy {
		c.trace.pass(gateSignalChannel, "the operator's signal Secret is projected here")
		return
	}
	var reasons []string
	if len(unreadable) > 0 {
		reasons = append(reasons, "signal directories cannot be read: "+joinSorted(unreadable))
	}
	if len(unprojected) > 0 {
		// F-86: an empty Secret projects identically to a missing one, so the marker is the only
		// way to tell "no flow is running" from "this channel does not exist".
		reasons = append(reasons, "no "+nodeagent.DeliveryChannelMarker+" marker in: "+joinSorted(unprojected)+
			" (the volume is not the operator's signal Secret, so no halt can arrive on it)")
	}
	c.trace.fail(gateSignalChannel, strings.Join(reasons, "; "))
}

func joinSorted(values []string) string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func roundedDuration(elapsed time.Duration) string {
	return elapsed.Round(time.Millisecond).String()
}
