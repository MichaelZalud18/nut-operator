# VM framework modules

These packages provide guest-independent primitives for the Hadron and Talos harnesses.
They compose with the existing adapters; they do not own scenario policy or start VMs implicitly.

| Module | Contract |
| --- | --- |
| `artifact` | Resolve local artifacts or download pinned HTTP(S) artifacts; publish only verified bytes without overwriting existing destinations. |
| `readiness` | Run immediate/repeated checks and periodic diagnostics under one bounded context, preserving the last failure. |
| `command` | Explicit environments, argument arrays, private kubeconfig selection, separate capped output, Linux process-group cancellation, and repository lookup. |
| `workspace` | Private editable subtree copies; never edit/restore shared-checkout files. Reject symlinks and special files inside the source tree. |
| `lifecycle` | Reverse-order owned-resource cleanup with an independent total budget, partial-start handling, retry, and diagnostic retention. |
| `kube` | Explicit-config clients, recorded cluster UID checks, node readiness, and pod/container identity observations with per-attempt bounds. |
| `signalfixture` | Clock-controlled invalid payloads and safe Secret patch encoding; assertions remain scenario-owned. |
| `image` | Explicit tag/digest parsing and common operand/manager build command plans; archive/registry delivery remains adapter-owned. |
| `workflow` | Common job-budget/cleanup-order checks, exercised against every checked-in Hadron/Talos smoke workflow. |
| `scenario` | Validated sequential steps, per-step/total deadlines, bounded failure diagnostics and cleanup, structured outcomes, partial-start and panic cleanup. |
| `diagnostics` | Explicit collectors, private retained bundles, per-stream byte limits, independent collector failures and total/per-collector deadlines. |
| `fixture` | Fresh namespace creation in an explicitly identified cluster; retained owner/UID checks, preconditioned deletion and observed disappearance. |
| `inventory` | Bounded source enumeration and exact/structural function-duplicate candidates, including smoke-tagged sources. |
| [vmprocess](../vmprocess/machine.go) | Retain verified QEMU ownership from startup through confirmed cleanup; never substitute PID-file lookup for process identity. |
| [Python emergency cleanup](../../../hack/vm_cleanup.py) | Independent pidfd cleanup for both guest smoke roots if the Go test process fails; preserve diagnostics. |

`artifact.Prepare` takes a source, optional HTTP client, and a caller-owned destination. Each run
supplies its own directory. Failed downloads remove their temporary file and retain other files.
Local artifacts stay in place and are never deleted by this module. Remote artifacts require
SHA-256 pins; unpinned local builds remain supported. Existing destinations are refused, including
symlinks. Publication uses a hard link in the destination directory, so the filesystem must
support hard links. A ten-minute ceiling bounds downloads; shorter caller deadlines win.

`readiness.Wait` takes an explicit timeout and polling interval. Its callbacks must honor the
provided context; the helper cannot forcibly interrupt a blocking callback. Diagnostics share the
same deadline and run synchronously. There are no detached goroutines or hidden cleanup actions.
Zero `DiagnoseEvery` means diagnostics follow every failed check; a nil callback disables them.

`command.Run` inherits no child environment automatically; callers explicitly provide required
`PATH`, `HOME`, Docker/Talos settings, and any private credentials. Executable lookup uses the
host process's PATH unless an absolute executable is provided. Returned errors omit command
arguments, stdin, and captured output; callers choose what diagnostics to publish. Output is
bounded separately for stdout/stderr. Linux cancellation terminates the owned process group;
non-Linux execution fails explicitly. Kubernetes command builders reject cluster-selection
overrides. They construct commands without running them.

`workspace.Clone` copies an explicitly chosen subtree into a fresh private directory. Choose a
bounded configuration/build tree, not an arbitrary large workspace. It does not supply a Git
worktree or promise a transactionally consistent snapshot while the source is being edited.
The caller owns the result and its cleanup; source files are never restored or rewritten.
`lifecycle.Scope` registers stop/remove callbacks before startup. Stop callbacks must verify
resource identity and honor their context. Failed stops retain all state, while successful
cleanup can retain diagnostics explicitly. It does not replace vmprocess or classify a process
exit as actuator success. Callback methods must not recursively access the same scope.

`kube.ReadyPod` is deliberately stricter than the current scenario helpers: PodRunning alone
does not suffice. A ready condition, ready/running named container, UID, and container ID are
required. Its comparable identity includes restart count. Adopting this helper changes the
observation contract and needs scenario qualification. Cluster UID checks require a trusted UID
recorded by the owner; they do not discover ownership by querying the cluster being checked.

Signal fixtures retain the current two-minute-TTL negative matrix's ten-minute time offsets.
They use the caller's clock and are checked against the real actuator parser. They prove fixture
content, not delivery/rejection by a live actuator. Image parsing supports explicit tags and
SHA-256 digests but is not a full OCI name validator; tag-only consumers reject digest references.

Workflow checks enforce the existing declared budget/order conventions. They do not interpret
arbitrary GitHub expressions or prove shell-command ownership. The historical kernel-only KVM
probe has a separate lifecycle and is inventoried rather than treated as a full guest smoke job.

`scenario.Run` validates every step before starting. A failed step prevents later steps and
suppresses resource-removal callbacks, while stop callbacks still run. Cleanup and optional
failure diagnostics use fresh contexts with explicit budgets after parent cancellation.
Diagnostics run before stops, so they can inspect live resources, and their budget is separate
from cleanup. The maximum cooperative duration is execution + diagnostics + cleanup budgets;
a timeout cannot forcibly interrupt a callback. Panics propagate after cleanup, retaining state;
failure diagnostics are skipped during panic unwinding. Reports keep step, diagnostic and
cleanup failures distinct, and none can silently become a successful result. A cleanup removal
failure can leave partially removed state; this is reported, not rolled back.

`diagnostics.Capture` only runs explicitly supplied collectors. It creates a new private directory
and mode-0600 files, retains partial output after failure, and caps bytes per collector. A callback
cannot hide truncation by ignoring its writer error. An individual collector failure does not
prevent other collectors within the total budget. Callers must redact streams before writing and
choose which artifacts to publish; callback errors are returned in memory, never automatically
written to disk. Collector writes must be serialized and finish before the callback returns.
Local filesystem calls are not forcibly cancellable. Artifact existence is not proof that a
collector succeeded: inspect the returned entry errors and truncation flags.

`fixture.CreateNamespace` requires an explicit client, trusted previously recorded cluster UID
and positive API-operation budget. It creates a generated-name namespace with a random owner token
and retains its server UID. A lost/ambiguous create response is not retried or adopted by name;
inspect retained diagnostics instead. `Check` rechecks identity before scenario work, but cannot
make arbitrary later caller API writes atomic with that check. `Delete` rechecks cluster and
namespace identity, sends UID and resource-version preconditions, and waits for disappearance.
Replacement, ownership changes, conflicts and stuck finalizers fail closed. No finalizer is
removed. Retry a timed-out deletion through the retained handle; never create a new name-only
cleanup handle. Register cleanup before creation and retain a nonnil returned handle even when
creation also reports cancellation. The module does not manage manifests, guest power or clusters.

The framework-only [composition test](scenario/composition_test.go) demonstrates a private
workspace, owned namespace, failed assertion, pre-cleanup diagnostic collection and retained
failure artifacts. It uses a fake Kubernetes client. Talos single-node boot also consumes
scenario/lifecycle and diagnostics; its manual workflow exercises real boot and cancellation
after verified startup. The namespace fixture remains standalone. Hadron retains its existing
scenario mechanics.

Run the primitive suites without guest tags:

```sh
go test -race ./test/internal/vmframework/... -count=1
```

Run the composition contracts against both real adapter constructors, plus process ownership:

```sh
go test -race -tags=hadron,talos ./test/internal/... ./test/hadron ./test/talos -count=1
python3 -B -m unittest discover -s hack -p test_vm_cleanup.py
```

The composition tests prepare pinned HTTP fixture images and pass them to each adapter. They
check configuration, private state, and artifact ownership without calling `Create`. They need
loopback sockets but no Docker, QEMU, KVM, or guest cluster. Process ownership unit tests use
harmless subprocesses. CI additionally requires the installed QEMU regression: a paused,
device-free QEMU process without a guest OS or KVM, checked immediately and after a short delay.
These results do not qualify live boot or actuator shutdown.

Reproduce the original harness inventory from the repository root:

```sh
go run ./test/internal/vmframework/cmd/inventory > /tmp/vm-inventory.json
```

All adapter Go functions are listed, including tests and build-tagged smoke code. Exact matches
ignore formatting/comments; structural candidates also normalize identifiers and literals.
The tool compares whole functions, not all possible repeated sub-blocks. Manual scenario/setup
analysis complements it. File hashes make the dated source snapshot auditable. Neither matching
shapes nor coverage counts establish semantic equivalence or complete live qualification.

See [framework design and extraction decisions](../../../docs/contributing/design/vm-test-framework.md)
for the source inventory and adoption sequence.
