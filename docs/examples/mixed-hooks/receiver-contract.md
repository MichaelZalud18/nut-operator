# External shutdown receiver contract

Components: Modular Deployment Profiles; Planning & Execution Logic.
Audience: integration authors.

This is the application contract for the [mixed-flow example](README.md), using the existing
[ShutdownHook protocol](../../contributing/design/shutdown-hooks.md). It adds no operator API.

## Request and authorization

Accept bounded HTTP POST requests using `application/cloudevents+json`. Authenticate the
Secret-backed Authorization header and authorize its principal for the explicitly named host
or receiver-owned group. Reject unknown targets, unsupported operations and malformed requests.
Do not interpret arbitrary request strings as shell commands or paths. The example sends
`data.extensions.host: external-a` and an explicit operation (`ensure-stopped` or `rehearse-stop`).

The event includes `data.executionID`, `planHash`, `shutdownFlow`, `group`, `waveIndex`,
`dryRun`, `rehearsalDeclared`, `selectedUPSDevices`, `power`, `hook`, `params` and `extensions`.
Use these to correlate receiver evidence with the operator's action audit. Selected UPS names
are contextual evidence, not an automatic external-host selector or authorization grant.

TLS protects the connection; verify the caller's credential independently. The operator's
endpoint allowlist limits where it can send requests, not which clients may invoke your service.
Never log Authorization values or include them in error responses. Permit network access only
from intended callers and make DNS, certificate trust and availability part of site setup.

## Rehearsal and repetition

Validate `data.dryRun` as a boolean. A rehearsal endpoint must not actuate, even if the request
claims enforce mode. An enforce endpoint must not actuate a dry-run request. The example's
separate `/hosts/rehearse` path should return evidence of validation/acceptance with no host effect.
Without a declared hook rehearsal invocation, the operator sends no request in dry-run.

Implement `ensure-stopped` as a repeat-safe operation based on the external system's actual
host or job state. Accepting a repeated request must not create conflicting shutdown work.
Event IDs can differ across retries, groups and executions; deduplicating that ID alone is
insufficient. Persist whatever operation state your service needs, and define host restart/rearm
outside this integration. The operator does not recover hosts or guarantee exactly-once delivery.

## Response and ordering

Return 2xx only when the bounded handoff has been accepted according to your service's contract.
Reject invalid credentials/targets explicitly and return a failure if the handoff was not accepted.
The operator records the HTTP delivery outcome; it does not follow a job URL, poll for completion,
or confirm that the machine powered off. A lost response can leave the handoff outcome ambiguous,
which is another reason to make repeated work safe.

Delivery timeout bounds waiting, not the lifetime of an already accepted external operation.
The operator continues after failure/timeout and marks the flow degraded. Explicit group ordering
means “attempt this handoff before the next group,” not “prove this host stopped first.” If later
node shutdown requires that proof, this advisory integration cannot provide the dependency.

The receiver owns actual host actuation and its own completion evidence. The repository's
receiver fixture is intentionally harmless and proves none of those physical effects.
