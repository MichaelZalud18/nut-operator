#!/usr/bin/env bash
set -euo pipefail

# Disposable, loopback-only database. No existing cluster or database is targeted.
cd "$(dirname "$0")/.."
image="${POSTGRES_TEST_IMAGE:-ghcr.io/cloudnative-pg/postgresql@sha256:78c4fdf165e8ffb1b5b7a7fc6b22b3cf37890a338b4c1c8c4d913896129a86da}"
container=""
network=""
cleanup() {
  local result=0
  if [[ -n "$container" ]]; then
    docker rm -f "$container" >/dev/null || result=1
  fi
  if [[ -n "$network" ]]; then
    docker network rm "$network" >/dev/null || result=1
  fi
  return "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
network=$(docker network create "audit-test-$$-$RANDOM")
container=$(docker run -d --rm --network "$network" -p 127.0.0.1::5432 \
  --entrypoint sh "$image" -c \
  'initdb -D /tmp/audit-test-db -A trust -U postgres >/dev/null && printf "host all all all trust\n" >> /tmp/audit-test-db/pg_hba.conf && exec postgres -D /tmp/audit-test-db -h 0.0.0.0')
address=$(docker port "$container" 5432/tcp)
export AUDIT_TEST_POSTGRES_DSN="postgres://postgres@${address}/postgres?sslmode=disable"
go test -tags=postgres ./internal/audit -run '^TestPostgres' -race -count=1 -timeout=2m
