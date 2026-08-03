# Trigger Evaluation

`internal/trigger` evaluates `ShutdownFlow` trigger conditions against normalized UPS state. It is
pure logic: callers provide the observation time, current UPS states, trigger definitions, and any
prior hold state.

## Boundary

Inputs:

- trigger definitions
- normalized UPS phase, charge, runtime, power domains, and stale markers
- prior hold state for `spec.triggers[].for`
- caller-provided observation time

Outputs:

- one decision per trigger
- an overall eligible/not-eligible result
- selected UPS devices for eligible triggers
- next hold state
- structured diagnostics

The package does not read Kubernetes resources, poll NUT, write audit records, start executors, or
read the wall clock.

## Semantics

A trigger is eligible when at least one selected UPS satisfies the trigger condition and its optional
hold duration has elapsed. `upsDeviceRefs` and `powerDomains` narrow selection; an empty selector
means all supplied UPS states.

`RuntimeBelow` and `ChargeBelow` require both a configured threshold and matching telemetry. Missing
thresholds are errors. Missing telemetry is a warning and does not produce an optimistic match.

## Runtime Integration

The controller layer adapts `ShutdownFlow.spec.triggers` and current `UPSDevice.status` into this
package, persists hold state, records `shutdownflow_decisions`, and dispatches eligible trigger
episodes to the executor. `ShutdownFlow.status.lastExecution.deduplicationKey` prevents repeated
execution while the same trigger episode remains eligible; the key becomes reusable after the
trigger clears and later becomes eligible again. Execution stays dry-run unless the flow and
node-agent actuation gates are both approved.
