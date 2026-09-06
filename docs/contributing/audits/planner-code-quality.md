# Planner Code Quality Audit

Findings from a 2026-08-25 sweep of `internal/planner`.

Scope: eight non-test planner files, with tests read only where they explained behavior. Baseline at
the time of the sweep: `go vet`, `golangci-lint`, and `go test` were clean; statement coverage was
92.0%.

The module shape is healthy: `Compile` is pure, structural inputs are deep-copied, and ordering is
deterministic. The remaining findings are local cleanup items.

## Findings

### `F-116` - `HistoryInputs` documents the wrong hash behavior

Severity: Medium. Fixed 2026-09-02.

`history.go` used to say resolved history became part of the plan hash. It now matches `compiler.go`,
`types.go`, and the history tests: history affects `ObservedDuration`, `GroupEstimates`, and estimate
confidence, but not `Hash` or `StructuralHash`.

### `F-117` - `compileGroups` has no no-progress guard

Severity: High. The path is protected by validation today, but if that invariant breaks the failure
mode is a hung planner compile during reconciliation.

`compileGroups` loops while `indegree` is non-empty. A cycle is rejected before this path today, but
if that invariant ever breaks the compiler would hang instead of returning a diagnostic.

Fix: add a no-progress guard and convert the failure into an error path.

*Closed 2026-09-02.* `compileGroups` now returns the groups it could not schedule alongside its
plan, and `CompileWithHistory` turns a non-empty list into a `PlanNotSchedulable` diagnostic naming
them and an `ErrRejected` return. The stall is rejected rather than published half-descended: a plan
missing the groups that never scheduled would still compile, still hash, and still look like a plan,
and the groups it dropped are the ones nothing would then shut down.

The message says the failure is a planner defect rather than a flow error, because a dependency cycle
is already rejected as `DependencyCycle` from the same `buildGroupGraph` output before this point.

Tested by calling `compileGroups` directly with a hand-built two-group cycle. Going through `Compile`
would exercise `DependencyCycle` and never reach the guard, which is why the hang went unnoticed:
every path a test could have reached it by was closed upstream. `compileGroups` is now at 100%
statement coverage. The rejection branch in `CompileWithHistory` is not reachable from the public
API without breaking the invariant it exists for, so package coverage moves 92.0% to 91.8% — the
uncovered lines are the ones that only run when this package is already wrong.

### `F-118` - `sortTriggers` hashes inside the comparator

Severity: Low. This is avoidable work and a readability problem, not a current scaling bottleneck.

`sortTriggers` computes `stableHash` for both operands on every comparison. Trigger counts are
small, but the function does repeated JSON marshal/hash work that is avoidable and hidden at the
call site.

Fix: hash each trigger once, sort the decorated values, then copy triggers back.

*Closed 2026-09-04.* `sortTriggers` now hashes each trigger once into a `[]struct{trigger Trigger;
hash string}`, sorts that by hash, then copies the triggers back out in the new order -- exactly
the fix the finding named. Combined with `F-123` below, since the rewrite already had to touch
every `stableHash` call site. `TestSortTriggersOrdersDeterministicallyByContentHash`
(`code_quality_test.go`) confirms the rewritten shape still produces the same
ascending-content-hash order the original inline-hashing comparator promised, and that every
trigger present before the sort is still present after it.

### `F-119` - dependency flattening appends into a slice it does not own

Severity: Low. The current callers make the aliasing harmless, but the expression is fragile.

`validateStructuralInputs` flattens dependencies with an append chain that can write into
`group.Before`'s backing array when it has spare capacity. The current callers make this harmless,
but the ownership assumption is implicit.

Fix: build an owned dependency slice directly. `slices.Concat` is available under the repo's Go
1.26 target.

*Closed 2026-09-04.* The append chain is now `slices.Concat(group.Requires, group.Before,
group.After)`, which always allocates its own backing array by contract -- no test added, because
the fix removes the aliasing risk by construction rather than by behavior a test could observe;
the existing `TestCompileRejectsUnknownDependency` already covers the dependency-checking behavior
itself and continues to pass unchanged.

### `F-120` - tier-0 diagnostics lose selector-rule context

Severity: Medium. The planner rejects the bad target, but the diagnostic may point users at the
wrong source of the bad tier.

The planner reports `ShutdownTierZeroTargeted` against the group. When the tier came from an adapter
selector rule, the group may not mention tier 0 at all.

Fix: decide whether selector-rule context belongs in planner inputs or whether the adapter should
own that diagnostic before calling the planner.

*Closed 2026-09-04, decided for the adapter.* `internal/planner` is documented as pure -- it never
does selector matching and has no other reason to know a `TierSelector` or a numeric-tier label
exists -- while `internal/shutdownflow/adapter.go`'s `PlannerShutdownTier` already resolves exactly
this at the moment it builds `planner.StructuralInputs`, before the group's tier is flattened into
a bare `*int32` the planner can no longer attribute. Threading rule identity into planner inputs
would have widened the pure package's type surface for a concept only one caller produces; keeping
it in the adapter, where the match already happens, does not.

New exported `TierPolicyDiagnostics` in `adapter.go` walks the same groups
`CompileFlowWithHistoryAndHooks` is about to compile and, for every group whose tier resolved to 0
through a selector rule or a numeric label rather than its own `spec.shutdownTier`, emits a
`ShutdownTierZeroFromPolicy` diagnostic naming the responsible rule or label key. It does not
replace the planner's own `ShutdownTierZeroTargeted` diagnostic -- both travel together in
`CompiledFlow.Diagnostics`, since the planner's still correctly says *that* the group targets tier
0 and the adapter's now says *why*. `matchingTierSelectorRule` was pulled out of
`shutdownTierFromSelectorRules` so tier resolution and diagnostic attribution share one definition
of "which rule matches first" rather than two that could drift apart.

Five new tests in `internal/shutdownflow/tier_policy_diagnostics_test.go`: the rule case, the label
case, that an explicit `spec.shutdownTier: 0` produces no adapter diagnostic (the planner's own
already names the right source), that a non-zero policy-resolved tier produces nothing, and an
end-to-end `CompileFlow` call confirming both diagnostics actually land together in one compiled
result rather than only in the standalone function. Webhook and controller-validation call sites
that invoke `planner.Compile` directly (`shutdownflow_webhook.go`, `controller/validation.go`)
were not wired to this -- they predate `CompiledFlow` and don't carry adapter diagnostics of any
kind today, so adding one selectively there would be new scope, not this finding's fix.

### `F-121` - duplicate IDs are reported once per repeat

Severity: Low. The validation result is correct; the output is noisier than it needs to be.

Duplicate step IDs and group names currently emit one diagnostic for each repeated occurrence after
the first.

Fix: report each duplicated identifier once.

*Closed 2026-09-04.* Both loops in `validateStructuralInputs` now track a second
`reported*` set alongside the existing seen-before set, so a third or later occurrence of the same
id no longer emits another diagnostic on top of the one already raised for the second.
`TestValidateStructuralInputsReportsEachDuplicateIdentifierOnce` (`code_quality_test.go`) uses
three occurrences of one step id and one group name -- two would have already passed under the old
behavior, since it only over-reports starting at the third -- and asserts exactly one diagnostic
each.

### `F-122` - `Duration.MarshalJSON` is separated from `Duration`

Severity: Low. This is code organization only.

`Duration` is declared in `types.go`, but its JSON method lives in `compiler.go`.

Fix: move `MarshalJSON` beside the type.

*Closed 2026-09-04.* Moved as described; no behavior change.
`TestDurationMarshalsAsHumanReadableString` locks in the encoding at its new location.

### `F-123` - `stableHash` panics in an otherwise error-returning module

Severity: Medium. The panic is effectively unreachable today, but it is inconsistent with the
planner's error-returning API and sits on a power-event path.

`stableHash` panics if `json.Marshal` fails. The input shapes make that effectively impossible
today, but `Compile` already returns errors and this path runs during power-event planning.

Fix: decide whether to keep the cannot-happen panic or thread an error through the hash callers.

*Closed 2026-09-04, decided to thread the error through.* Same call this project already made for
`F-117`'s no-progress guard: a "cannot happen" invariant on a power-event path is worth converting
into a diagnosable rejection rather than trusting it, because the failure mode if the invariant
ever breaks anyway is a lot worse than the cost of handling an error return that mostly returns
nil. `stableHash` now returns `(string, error)`; its three callers (`sortTriggers`,
`plan.StructuralHash`, `plan.Hash`) all propagate a failure into a `TriggerEncodingFailed` or
`PlanHashEncodingFailed` diagnostic and `ErrRejected`, matching the shape every other rejection in
`CompileWithHistory` already takes. `TestStableHashReturnsErrorRatherThanPanicking` proves the
mechanism directly, with a `chan int` handed to `stableHash` -- the only way to observe this
branch at all, since every real field the planner ever hashes (strings, string slices, int
pointers, `Duration`'s own string-encoding `MarshalJSON`) is exactly the kind of value that cannot
fail to marshal, which is what made this "effectively impossible today" in the first place.
