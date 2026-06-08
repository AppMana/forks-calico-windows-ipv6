#!/bin/bash
# Run the validated AppMana kind + QEMU Calico health matrix.

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../.." && pwd)

export KUBECONFIG="${KUBECONFIG:-/tmp/appmana-calico-kind/kubeconfig}"
NAMESPACE="${NAMESPACE:-calico-qemu-test}"
IPV4_POOL="${IPV4_POOL:-kind-ipv4-pool}"
IPV6_POOL="${IPV6_POOL:-kind-ipv6-pool}"
LINUX_IMAGE="${LINUX_IMAGE:-nicolaka/netshoot:latest}"
WIN_IMAGE="${WIN_IMAGE:-mcr.microsoft.com/windows/servercore:ltsc2022}"
WINDOWS_EXEC="${WINDOWS_EXEC:-hcsdiag}"
APPLY_FORWARDING="${APPLY_FORWARDING:-true}"
IPV4_ONLY="${IPV4_ONLY:-false}"
PREFLIGHT="${PREFLIGHT:-true}"

if [[ $# -gt 0 ]]; then
  NODES=("$@")
else
  NODES=("kind-worker2" "appmana-000")
fi

if [[ "$APPLY_FORWARDING" == "true" ]]; then
  "${FORWARDING_SCRIPT:-$SCRIPT_DIR/apply-kind-qemu-forwarding.sh}"
fi

if [[ "$PREFLIGHT" == "true" ]]; then
  "${PREFLIGHT_SCRIPT:-$SCRIPT_DIR/check-kind-qemu-calico-ready.sh}"
fi

kubectl --kubeconfig "$KUBECONFIG" create namespace "$NAMESPACE" \
  --dry-run=client -o yaml | kubectl --kubeconfig "$KUBECONFIG" apply -f -

args=(
  --namespace "$NAMESPACE" \
  --ipv4-pool "$IPV4_POOL" \
  --windows-exec "$WINDOWS_EXEC" \
  --linux-image "$LINUX_IMAGE" \
  --win-image "$WIN_IMAGE"
)

if [[ "$IPV4_ONLY" == "true" ]]; then
  args+=(--ipv4-only)
else
  args+=(--ipv6-pool "$IPV6_POOL")
fi

exec "${HEALTH_CHECK_SCRIPT:-$REPO_ROOT/hack/appmana/ipv6-health-check.sh}" \
  "${args[@]}" \
  "${NODES[@]}"
