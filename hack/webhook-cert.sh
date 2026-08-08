#!/usr/bin/env bash
#
# Provision the admission webhook serving certificate without cert-manager.
#
# This is the certificate path for the config/byo-cert overlay. It exists because this operator has
# to keep working while the cluster is losing power: during a tiered shutdown the workloads it
# depends on are themselves being stopped, so anything that must reconcile, issue, or inject before
# admission works again is a component that can be unavailable at the worst possible moment. A
# serving certificate that is only a static Secret has no such moment.
#
# Admission webhook certificates are not validated against any public or cluster trust store. The
# API server validates the webhook's certificate against .webhooks[].clientConfig.caBundle on the
# webhook configuration object, which only a cluster administrator can write. A privately issued CA
# is therefore the correct shape here, not a compromise -- cert-manager's own default for this job
# is a selfSigned Issuer.
#
# The script is idempotent and is also the rotation procedure: re-run it. The CA is reused across
# runs so caBundle stays stable, and the manager picks up a rewritten serving certificate live
# through its controller-runtime certwatcher, with no restart.

set -euo pipefail

namespace="nut-operator-system"
service="nut-operator-webhook-service"
secret="webhook-server-cert"
ca_secret="nut-operator-webhook-ca"
mutating="nut-operator-mutating-webhook-configuration"
validating="nut-operator-validating-webhook-configuration"
ca_days=3650
cert_days=365
ca_cert_file=""
ca_key_file=""
kubectl_bin="${KUBECTL:-kubectl}"

usage() {
  cat >&2 <<'EOF'
usage: webhook-cert.sh [options]

Mints the webhook serving certificate, writes it to a Secret, and patches caBundle into both
webhook configurations. Re-run to rotate.

  --namespace NAME      Operator namespace (default: nut-operator-system)
  --service NAME        Webhook Service name (default: nut-operator-webhook-service)
  --secret NAME         Serving certificate Secret (default: webhook-server-cert)
  --ca-secret NAME      Secret holding the generated CA (default: nut-operator-webhook-ca)
  --ca-cert FILE        Use an external CA certificate instead of generating one.
  --ca-key FILE         Private key for --ca-cert. Neither is stored in the cluster.
  --ca-days N           Generated CA lifetime in days (default: 3650)
  --cert-days N         Serving certificate lifetime in days (default: 365)
  -h, --help            This message.

Environment: KUBECTL overrides the kubectl binary.
EOF
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --namespace) namespace="$2"; shift 2 ;;
    --service) service="$2"; shift 2 ;;
    --secret) secret="$2"; shift 2 ;;
    --ca-secret) ca_secret="$2"; shift 2 ;;
    --ca-cert) ca_cert_file="$2"; shift 2 ;;
    --ca-key) ca_key_file="$2"; shift 2 ;;
    --ca-days) ca_days="$2"; shift 2 ;;
    --cert-days) cert_days="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 64 ;;
  esac
done

if [[ -n "$ca_cert_file" && -z "$ca_key_file" ]] || [[ -z "$ca_cert_file" && -n "$ca_key_file" ]]; then
  echo "--ca-cert and --ca-key must be given together" >&2
  exit 64
fi

for tool in openssl "$kubectl_bin"; do
  command -v "$tool" >/dev/null || { echo "required tool not found: $tool" >&2; exit 69; }
done

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
chmod 700 "$workdir"

# The API server dials the webhook by Service DNS, so these two names are what the certificate has
# to carry. A missing SAN surfaces much later as an opaque x509 error on every CR write.
cat >"$workdir/openssl.cnf" <<EOF
[req]
distinguished_name = dn
prompt             = no
[dn]
CN = ${service}.${namespace}.svc
[ext]
basicConstraints       = critical,CA:FALSE
keyUsage               = critical,digitalSignature,keyEncipherment
extendedKeyUsage       = serverAuth
subjectAltName         = DNS:${service}.${namespace}.svc,DNS:${service}.${namespace}.svc.cluster.local
EOF

if [[ -n "$ca_cert_file" ]]; then
  echo "Using external CA from ${ca_cert_file}"
  cp "$ca_cert_file" "$workdir/ca.crt"
  cp "$ca_key_file" "$workdir/ca.key"
elif "$kubectl_bin" -n "$namespace" get secret "$ca_secret" >/dev/null 2>&1; then
  # Reusing the CA is what keeps caBundle stable across rotations, so a routine serving-certificate
  # renewal never has to touch the webhook configurations.
  echo "Reusing CA from secret ${namespace}/${ca_secret}"
  "$kubectl_bin" -n "$namespace" get secret "$ca_secret" -o jsonpath='{.data.tls\.crt}' | base64 -d >"$workdir/ca.crt"
  "$kubectl_bin" -n "$namespace" get secret "$ca_secret" -o jsonpath='{.data.tls\.key}' | base64 -d >"$workdir/ca.key"
else
  echo "Generating CA (${ca_days}d)"
  openssl req -x509 -newkey rsa:4096 -nodes -days "$ca_days" \
    -keyout "$workdir/ca.key" -out "$workdir/ca.crt" \
    -subj "/CN=nut-operator webhook CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
fi

echo "Issuing serving certificate for ${service}.${namespace}.svc (${cert_days}d)"
openssl req -new -newkey rsa:2048 -nodes \
  -keyout "$workdir/tls.key" -out "$workdir/tls.csr" \
  -config "$workdir/openssl.cnf" 2>/dev/null
openssl x509 -req -in "$workdir/tls.csr" -days "$cert_days" \
  -CA "$workdir/ca.crt" -CAkey "$workdir/ca.key" -CAcreateserial \
  -extfile "$workdir/openssl.cnf" -extensions ext \
  -out "$workdir/tls.crt" 2>/dev/null

"$kubectl_bin" create namespace "$namespace" --dry-run=client -o yaml | "$kubectl_bin" apply -f - >/dev/null

if [[ -z "$ca_cert_file" ]]; then
  "$kubectl_bin" -n "$namespace" create secret tls "$ca_secret" \
    --cert="$workdir/ca.crt" --key="$workdir/ca.key" \
    --dry-run=client -o yaml | "$kubectl_bin" apply -f - >/dev/null
  echo "CA stored in secret ${namespace}/${ca_secret}"
fi

"$kubectl_bin" -n "$namespace" create secret tls "$secret" \
  --cert="$workdir/tls.crt" --key="$workdir/tls.key" \
  --dry-run=client -o yaml | "$kubectl_bin" apply -f - >/dev/null
echo "Serving certificate stored in secret ${namespace}/${secret}"

# Every webhook entry in a configuration shares one Service and one certificate here, so each gets
# the same bundle. Patching by index keeps this working if entries are added later.
ca_bundle="$(base64 -w0 <"$workdir/ca.crt" 2>/dev/null || base64 <"$workdir/ca.crt" | tr -d '\n')"
for config_kind in mutatingwebhookconfiguration validatingwebhookconfiguration; do
  case "$config_kind" in
    mutating*) config_name="$mutating" ;;
    *) config_name="$validating" ;;
  esac
  if ! "$kubectl_bin" get "$config_kind" "$config_name" >/dev/null 2>&1; then
    echo "Skipping ${config_kind}/${config_name}: not found. Apply the manifests, then re-run." >&2
    continue
  fi
  count="$("$kubectl_bin" get "$config_kind" "$config_name" -o jsonpath='{range .webhooks[*]}{.name}{"\n"}{end}' | grep -c . || true)"
  patch="["
  for ((i = 0; i < count; i++)); do
    [[ "$i" -gt 0 ]] && patch+=","
    patch+="{\"op\":\"replace\",\"path\":\"/webhooks/${i}/clientConfig/caBundle\",\"value\":\"${ca_bundle}\"}"
  done
  patch+="]"
  "$kubectl_bin" patch "$config_kind" "$config_name" --type='json' -p="$patch" >/dev/null
  echo "Injected caBundle into ${config_kind}/${config_name} (${count} webhooks)"
done

echo
echo "Done. The manager reloads the serving certificate without a restart."
echo "Re-run this script to rotate; the CA is reused so caBundle stays valid."
