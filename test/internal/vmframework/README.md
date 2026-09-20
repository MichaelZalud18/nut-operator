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
