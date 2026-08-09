#!/usr/bin/env bash
# Prove the NUT operands actually speak TLS.
#
# F-39: the operator rendered CERTFILE into upsd.conf and the packaged operand
# rejected it as an invalid directive, because that directive is OpenSSL-only and
# the package was an NSS build. Every render test passed the whole time -- they
# asserted the operator emits the directive, never that NUT honors it.
#
# So this test refuses to look at rendered configuration. It starts the real images
# with the file shapes the operator produces and speaks the protocol over the wire:
# the server must accept STARTTLS and complete a handshake against a pinned CA, and
# the agent must connect with CERTVERIFY and FORCESSL on. Neither half can pass on
# an image that cannot do the thing.
set -euo pipefail

if [[ "$#" -lt 2 || "$#" -gt 3 ]]; then
  echo "usage: nut-tls-smoke.sh <container-tool> <server-image> [agent-image]" >&2
  exit 64
fi

CONTAINER_TOOL="$1"
SERVER_IMAGE="$2"
AGENT_IMAGE="${3:-}"

RUN_ID="$$"
SERVER_NAME="nut-tls-smoke-server-${RUN_ID}"
AGENT_NAME="nut-tls-smoke-agent-${RUN_ID}"
NETWORK_NAME="nut-tls-smoke-net-${RUN_ID}"
# The certificate has to name whatever the agent dials, because CERTVERIFY checks it.
SERVER_ALIAS="nut-server"
WORKDIR="$(mktemp -d)"

cleanup() {
  "${CONTAINER_TOOL}" rm -f "${SERVER_NAME}" "${AGENT_NAME}" >/dev/null 2>&1 || true
  "${CONTAINER_TOOL}" network rm "${NETWORK_NAME}" >/dev/null 2>&1 || true
  rm -rf "${WORKDIR}"
}
trap cleanup EXIT

echo "==> generating a CA and server certificate"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj "/CN=nut-tls-smoke-ca" \
  -keyout "${WORKDIR}/ca.key" -out "${WORKDIR}/ca.crt" >/dev/null 2>&1

openssl req -newkey rsa:2048 -nodes \
  -subj "/CN=${SERVER_ALIAS}" \
  -keyout "${WORKDIR}/tls.key" -out "${WORKDIR}/tls.csr" >/dev/null 2>&1

cat > "${WORKDIR}/ext.cnf" <<EOF
subjectAltName = DNS:${SERVER_ALIAS}, DNS:localhost, IP:127.0.0.1
extendedKeyUsage = serverAuth
EOF

openssl x509 -req -in "${WORKDIR}/tls.csr" \
  -CA "${WORKDIR}/ca.crt" -CAkey "${WORKDIR}/ca.key" -CAcreateserial \
  -days 1 -extfile "${WORKDIR}/ext.cnf" \
  -out "${WORKDIR}/tls.crt" >/dev/null 2>&1

# Same shape the operator's init container assembles: subject certificate first,
# private key last, one file. Key-first is what cert-manager's CombinedPEM emits and
# is deliberately not what is tested here.
mkdir -p "${WORKDIR}/nut"
cat "${WORKDIR}/tls.crt" "${WORKDIR}/tls.key" > "${WORKDIR}/nut/combined.pem"
# NUT passes CERTPATH to SSL_CTX_load_verify_locations as OpenSSL's CApath -- a
# directory of hash-named CA files -- never as CAfile, whatever upsmon.conf(5) says.
# A single PEM there loads without error and then fails every verification.
mkdir -p "${WORKDIR}/nut/ca-dir"
cp "${WORKDIR}/ca.crt" "${WORKDIR}/nut/ca-dir/ca.crt"
openssl rehash "${WORKDIR}/nut/ca-dir" >/dev/null 2>&1

echo "==> writing the configuration the operator renders"
cat > "${WORKDIR}/nut/upsd.conf" <<EOF
LISTEN 0.0.0.0 3493
CERTFILE /etc/nut/tls/combined.pem
DISABLE_WEAK_SSL 1
EOF

cat > "${WORKDIR}/nut/ups.conf" <<'EOF'
[smokeups]
  driver = dummy-ups
  port = smoke.dev
  desc = "tls smoke"
EOF

cat > "${WORKDIR}/nut/smoke.dev" <<'EOF'
ups.status: OL
battery.charge: 100
EOF

cat > "${WORKDIR}/nut/upsd.users" <<'EOF'
[smokemon]
  password = smokepass
  upsmon primary
EOF

cat > "${WORKDIR}/nut/upsmon.conf" <<EOF
MONITOR smokeups@${SERVER_ALIAS} 1 smokemon smokepass secondary
MINSUPPLIES 1
SHUTDOWNCMD "/bin/true"
POLLFREQ 5
POLLFREQALERT 5
DEADTIME 15
CERTPATH /etc/nut/tls/ca-dir
CERTVERIFY 1
FORCESSL 1
EOF

chmod 0644 "${WORKDIR}"/nut/*.conf "${WORKDIR}"/nut/*.pem "${WORKDIR}"/nut/upsd.users "${WORKDIR}"/nut/smoke.dev
chmod -R 0755 "${WORKDIR}/nut/ca-dir"

"${CONTAINER_TOOL}" network create "${NETWORK_NAME}" >/dev/null

echo "==> starting server ${SERVER_IMAGE}"
"${CONTAINER_TOOL}" run -d --name "${SERVER_NAME}" \
  --network "${NETWORK_NAME}" --network-alias "${SERVER_ALIAS}" \
  -p 127.0.0.1::3493 \
  -v "${WORKDIR}/nut/ups.conf:/etc/nut/ups.conf:ro" \
  -v "${WORKDIR}/nut/upsd.conf:/etc/nut/upsd.conf:ro" \
  -v "${WORKDIR}/nut/upsd.users:/etc/nut/upsd.users:ro" \
  -v "${WORKDIR}/nut/smoke.dev:/etc/nut/smoke.dev:ro" \
  -v "${WORKDIR}/nut/combined.pem:/etc/nut/tls/combined.pem:ro" \
  "${SERVER_IMAGE}" >/dev/null

PORT="$("${CONTAINER_TOOL}" port "${SERVER_NAME}" 3493/tcp | head -1 | sed 's/.*://')"
if [[ -z "${PORT}" ]]; then
  echo "FAIL: could not determine the published port" >&2
  "${CONTAINER_TOOL}" logs "${SERVER_NAME}" >&2
  exit 1
fi

for _ in $(seq 1 30); do
  if "${CONTAINER_TOOL}" logs "${SERVER_NAME}" 2>&1 | grep -q "listening on"; then
    break
  fi
  sleep 1
done

# The exact symptom of F-39. upsd logs this and keeps serving plaintext, so it has
# to be a hard failure rather than something a reader might scroll past.
if "${CONTAINER_TOOL}" logs "${SERVER_NAME}" 2>&1 | grep -q "invalid directive"; then
  echo "FAIL: upsd rejected a directive the operator renders" >&2
  "${CONTAINER_TOOL}" logs "${SERVER_NAME}" 2>&1 | grep "invalid directive" >&2
  exit 1
fi

echo "==> probing the NUT protocol on 127.0.0.1:${PORT}"
CA_FILE="${WORKDIR}/ca.crt" PROBE_PORT="${PORT}" SERVER_ALIAS="${SERVER_ALIAS}" python3 - <<'PROBE'
import os
import socket
import ssl
import sys

port = int(os.environ["PROBE_PORT"])
ca_file = os.environ["CA_FILE"]
alias = os.environ["SERVER_ALIAS"]

sock = socket.create_connection(("127.0.0.1", port), timeout=15)
sock.settimeout(15)

sock.sendall(b"STARTTLS\n")
reply = sock.recv(256).decode(errors="replace").strip()
if not reply.startswith("OK"):
    print(f"FAIL: upsd refused STARTTLS: {reply!r}", file=sys.stderr)
    print("      an NSS build answers ERR FEATURE-NOT-CONFIGURED here", file=sys.stderr)
    sys.exit(1)
print(f"    STARTTLS accepted: {reply}")

context = ssl.create_default_context(cafile=ca_file)
context.minimum_version = ssl.TLSVersion.TLSv1_2
try:
    tls = context.wrap_socket(sock, server_hostname=alias)
except ssl.SSLError as err:
    print(f"FAIL: TLS handshake did not complete: {err}", file=sys.stderr)
    sys.exit(1)

print(f"    handshake completed: {tls.version()} {tls.cipher()[0]}")
if tls.version() in ("SSLv3", "TLSv1", "TLSv1.1"):
    print(f"FAIL: DISABLE_WEAK_SSL did not take effect: {tls.version()}", file=sys.stderr)
    sys.exit(1)

# Proving the channel carries the protocol, not just that it negotiated. The reply
# spans several segments, so it is read to its terminator rather than once.
tls.sendall(b"LIST UPS\n")
listing = ""
while "END LIST UPS" not in listing:
    chunk = tls.recv(4096).decode(errors="replace")
    if not chunk:
        break
    listing += chunk
if "smokeups" not in listing:
    print(f"FAIL: LIST UPS over TLS returned {listing!r}", file=sys.stderr)
    sys.exit(1)
print("    LIST UPS succeeded inside the TLS session")

tls.sendall(b"LOGOUT\n")
tls.close()
PROBE

if [[ -z "${AGENT_IMAGE}" ]]; then
  echo "PASS: ${SERVER_IMAGE} serves NUT over TLS (agent image not supplied)"
  exit 0
fi

echo "==> starting agent ${AGENT_IMAGE} with CERTVERIFY and FORCESSL"
"${CONTAINER_TOOL}" run -d --name "${AGENT_NAME}" \
  --network "${NETWORK_NAME}" \
  -v "${WORKDIR}/nut/upsmon.conf:/etc/nut/upsmon.conf:ro" \
  -v "${WORKDIR}/nut/ca-dir:/etc/nut/tls/ca-dir:ro" \
  --entrypoint /usr/sbin/upsmon \
  "${AGENT_IMAGE}" -D >/dev/null

AGENT_CONNECTED=""
for _ in $(seq 1 30); do
  AGENT_LOGS="$("${CONTAINER_TOOL}" logs "${AGENT_NAME}" 2>&1)"
  if grep -qi "Connected to NUT server .* in SSL" <<<"${AGENT_LOGS}"; then
    AGENT_CONNECTED="yes"
    break
  fi
  if grep -qi "SSL error\|certificate verify failed\|can not connect\|Poll UPS .* failed" <<<"${AGENT_LOGS}"; then
    break
  fi
  sleep 1
done

AGENT_LOGS="$("${CONTAINER_TOOL}" logs "${AGENT_NAME}" 2>&1)"
if [[ -z "${AGENT_CONNECTED}" ]]; then
  echo "FAIL: upsmon did not establish a verified TLS session with upsd" >&2
  echo "${AGENT_LOGS}" >&2
  exit 1
fi

# Connecting is not enough: CERTVERIFY has to have actually verified. upsmon says so
# explicitly, and without that line a connection proves only that SSL happened, not
# that the server was the one we pinned.
if ! grep -qi "Certificate verification (by client) is enabled, and apparently succeeded" <<<"${AGENT_LOGS}"; then
  echo "FAIL: upsmon connected but did not verify the server certificate" >&2
  echo "${AGENT_LOGS}" >&2
  exit 1
fi
echo "    upsmon connected over SSL and verified the server certificate"

echo "PASS: ${SERVER_IMAGE} and ${AGENT_IMAGE} speak NUT over verified TLS"
