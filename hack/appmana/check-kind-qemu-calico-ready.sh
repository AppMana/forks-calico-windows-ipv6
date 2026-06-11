#!/bin/bash
# Fail fast unless the kind/QEMU Windows networking baseline is actually ready.

set -euo pipefail

KUBECONFIG="${KUBECONFIG:-/tmp/appmana-calico-kind/kubeconfig}"
WINDOWS_NODE="${WINDOWS_NODE:-appmana-000}"
SSH_USER="${SSH_USER:-administrator}"
READY_TIMEOUT="${READY_TIMEOUT:-5s}"

first_ipv4() {
  tr ' ' '\n' | awk '/^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ { print; exit }'
}

WINDOWS_HOST="${WINDOWS_HOST:-}"
if [[ -z "$WINDOWS_HOST" ]]; then
  WINDOWS_HOST=$(
    kubectl --kubeconfig "$KUBECONFIG" get node "$WINDOWS_NODE" \
      -o jsonpath='{range .status.addresses[?(@.type=="InternalIP")]}{.address}{" "}{end}' |
      first_ipv4
  )
fi

if [[ -z "$WINDOWS_HOST" ]]; then
  echo "ERROR: could not determine IPv4 InternalIP for Windows node $WINDOWS_NODE" >&2
  exit 1
fi

kubectl --kubeconfig "$KUBECONFIG" -n kube-system wait \
  --for=condition=Ready pod \
  -l k8s-app=calico-node-windows \
  --timeout="$READY_TIMEOUT"

kubectl --kubeconfig "$KUBECONFIG" -n kube-system wait \
  --for=condition=Ready pod \
  -l k8s-app=kube-proxy-windows \
  --timeout="$READY_TIMEOUT"

# The probe must be a single line: the Windows default ssh shell is cmd.exe,
# which truncates a multiline command at the first newline, leaving PowerShell
# with an empty -Command (silent exit 0, empty output, false "missing" result).
status=$(
  ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "$SSH_USER@$WINDOWS_HOST" \
    'powershell -NoProfile -Command "$ErrorActionPreference = \"Stop\"; $nodename = Test-Path C:\CalicoWindows\nodename; $network = [bool](Get-HnsNetwork | Where-Object { $_.Name -eq \"Calico\" }); $endpoint = [bool](Get-HnsEndpoint | Where-Object { $_.Name -eq \"Calico_ep\" }); \"nodename=$nodename\"; \"hns_calico=$network\"; \"calico_ep=$endpoint\""'
)

echo "$status"

missing=()
grep -Fq 'nodename=True' <<<"$status" || missing+=("C:\\CalicoWindows\\nodename")
grep -Fq 'hns_calico=True' <<<"$status" || missing+=("HNS Calico network")
grep -Fq 'calico_ep=True' <<<"$status" || missing+=("HNS Calico_ep endpoint")

if (( ${#missing[@]} > 0 )); then
  printf 'ERROR: Windows Calico preflight failed; missing: %s\n' "${missing[*]}" >&2
  printf 'This is a bootstrap failure, not a pod/service health-matrix result.\n' >&2
  exit 1
fi
