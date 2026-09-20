# VM framework modules

These packages provide guest-independent primitives for the Hadron and Talos harnesses.
They compose with the existing adapters; they do not own scenario policy or start VMs implicitly.

| Module | Contract |
| --- | --- |
| `artifact` | Resolve local artifacts or download pinned HTTP(S) artifacts; publish only verified bytes without overwriting existing destinations. |
| `readiness` | Run immediate/repeated checks and periodic diagnostics under one bounded context, preserving the last failure. |
| [vmprocess](../vmprocess/machine.go) | Retain verified QEMU ownership from startup through confirmed cleanup; never substitute PID-file lookup for process identity. |

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

Run the primitive suites without guest tags:

```sh
go test -race ./test/internal/vmframework/... -count=1
```

Run the composition contracts against both real adapter constructors, plus process ownership:

```sh
go test -race -tags=hadron,talos ./test/internal/... ./test/hadron ./test/talos -count=1
```

The composition tests prepare pinned HTTP fixture images and pass them to each adapter. They
check configuration, private state, and artifact ownership without calling `Create`. They need
loopback sockets but no Docker, QEMU, KVM, or guest cluster. Process tests use harmless subprocesses.
These results do not qualify live boot or actuator shutdown.

See [framework design and extraction decisions](../../../docs/contributing/design/vm-test-framework.md)
for the source inventory and adoption sequence.
