# Disposable NetBox integration

This conditional service suite runs the shipped `netbox-inventory-sync` CLI against a real,
isolated NetBox REST API. It is separate from Kind and the shutdown runtime. It never accepts
an existing service URL, site token, database, container, or network.

```sh
make test-netbox-harness
make test-netbox
```

The service test requires Python 3, Go from `go.mod`, Docker with registry access, and a Linux
amd64 or arm64 daemon. It cross-compiles static CLI/test binaries for the daemon architecture
and copies them into the disposable NetBox container. HTTP requests stay on container loopback;
PostgreSQL and Redis use a uniquely named internal Docker network. No ports are published,
host directories mounted, host sysctls changed, or Kubernetes resources created.

## Dependency compatibility

Exact versions and multi-architecture registry digests live only in
[`hack/test-netbox.py`](../../hack/test-netbox.py). Upgrade them together and rerun the real suite.
The compatibility basis, checked against primary upstream sources on 2026-09-17, is:

- [NetBox Docker release compatibility](https://github.com/netbox-community/netbox-docker/releases/tag/5.1.1)
  pairs this container release with NetBox 4.7 and later.
- [NetBox's dependency matrix](https://netbox.readthedocs.io/en/stable/installation/upgrading/)
  requires PostgreSQL 15+ and Redis 6+ for that series.
- Current patch tags in the supported PostgreSQL 17 and Redis 7.4 series were checked against
  the [official PostgreSQL image manifest](https://github.com/docker-library/official-images/blob/master/library/postgres)
  and [official Redis image manifest](https://github.com/docker-library/official-images/blob/master/library/redis).
  Registry index digests include both local arm64 and CI amd64 images.
- Bootstrap uses the image's entrypoint/migrations and the upstream
  [token model](https://github.com/netbox-community/netbox/blob/v4.7.0/netbox/users/models/tokens.py).
  Only disposable authentication is bootstrapped through Django; inventory fixtures use REST.

## Assertions

The fixture creates tagged UPS, Kubernetes-node and switch devices, JSON `nut_operator` metadata,
power outlets cabled to downstream `PSU-A` inputs, and physical interface cables. An untagged
device with invalid metadata proves selection. The server caps pages to one object, including
per-device power-port/interface endpoints, so the CLI must follow genuine API pagination.

The tests exercise read-only Bearer v2 and legacy Token v1 credentials, missing/invalid tokens,
canonical node identity, deduplicated communication edges, power-input identity, deterministic
snapshots and rendered CRs under reversed device ordering, and compilation against an authored
provider-neutral reference. Snapshot observation timestamps are checked then normalized only for
repeat comparisons; the timestamp is intentionally part of the compiler hash.

Malformed metadata, unsupported kinds, missing required UPS metadata, and an unmapped secondary
supply (even with another valid feed) must fail with empty stdout. Removing both node power cables
also exercises orphan rejection by the inventory compiler. Diagnostics are checked for credential
leakage. No real equipment, NUT server, actuation, Kubernetes admission, or deployed controller
behavior is exercised.

## Lifetime and credentials

Each run generates a UUID ownership label, service secrets and API tokens. Temporary host credential
files use a private directory and mode 0600; tokens stay inside the disposable container. The CLI gets
only its test credential environment. Child command output is withheld on harness failures;
the integration test emits fixed assertion messages, never API bodies or secret values.

The harness has a 20-minute total deadline and bounded build, pull, readiness, API, CLI and Docker
operations. Child commands run in separate process groups, killed on cancellation or timeout.
Cleanup uses a separately bounded pass after success, error, SIGINT or SIGTERM, verifies each
resource's ownership label, then removes containers (including anonymous volumes) before the
network. Cleanup failure makes the run fail and names the remaining candidates. SIGKILL or daemon
loss cannot guarantee cleanup; use the printed owner identifier to inspect exact resources and
verify their label before removal. Never prune shared Docker resources.

`go test ./test/netbox` includes the Docker-free Python harness tests; the real test is skipped
unless launched inside this harness. The dedicated workflow uses the Make targets with read-only
repository permissions, explicit action pins, path filters, and a job deadline allowing cleanup.
Passing this suite establishes compatibility for the pinned fixture, not every NetBox version.
