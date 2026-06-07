#!/bin/bash
# Configure host and Windows routes for the local kind + QEMU Calico lab.
#
# This is lab-only glue. It allows traffic between the Windows QEMU node on br0
# and Linux kind workers behind Docker's kind bridge.

set -euo pipefail

KUBECONFIG="${KUBECONFIG:-/tmp/appmana-calico-kind/kubeconfig}"
WIN_NODE_NAME="${WIN_NODE_NAME:-appmana-000}"
WIN_NODE_IP="${WIN_NODE_IP:-}"
WIN_SSH_USER="${WIN_SSH_USER:-administrator}"
HOST_BRIDGE="${HOST_BRIDGE:-br0}"
HOST_BR0_IP="${HOST_BR0_IP:-}"
KIND_NETWORK_NAME="${KIND_NETWORK_NAME:-kind}"
KIND_BRIDGE="${KIND_BRIDGE:-}"
POD_CIDR="${POD_CIDR:-10.244.0.0/16}"
WINDOWS_POD_BLOCK="${WINDOWS_POD_BLOCK:-}"
LINUX_POD_BLOCKS="${LINUX_POD_BLOCKS:-}"

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "ERROR: missing required command: $1" >&2
    exit 1
  }
}

require kubectl
require docker
require python3
require ip
require ssh
require sudo

if [[ ! -f "$KUBECONFIG" ]]; then
  echo "ERROR: KUBECONFIG does not exist: $KUBECONFIG" >&2
  exit 1
fi

if [[ -z "$WIN_NODE_IP" ]]; then
  WIN_NODE_IP=$(kubectl --kubeconfig "$KUBECONFIG" get node "$WIN_NODE_NAME" \
    -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)
fi
if [[ -z "$WIN_NODE_IP" ]]; then
  WIN_NODE_IP="10.2.0.180"
fi

if [[ -z "$HOST_BR0_IP" ]]; then
  HOST_BR0_IP=$(ip -4 -o addr show dev "$HOST_BRIDGE" | awk '{sub(/\/.*/, "", $4); print $4; exit}')
fi
if [[ -z "$HOST_BR0_IP" ]]; then
  HOST_BR0_IP="10.2.0.55"
fi

if [[ -z "$KIND_BRIDGE" ]]; then
  bridge_name=$(docker network inspect "$KIND_NETWORK_NAME" \
    -f '{{ index .Options "com.docker.network.bridge.name" }}' 2>/dev/null || true)
  if [[ -n "$bridge_name" && "$bridge_name" != "<no value>" ]]; then
    KIND_BRIDGE="$bridge_name"
  else
    network_id=$(docker network inspect "$KIND_NETWORK_NAME" -f '{{ .Id }}' 2>/dev/null || true)
    KIND_BRIDGE="br-${network_id:0:12}"
  fi
fi

KIND_SUBNET=$(docker network inspect "$KIND_NETWORK_NAME" 2>/dev/null | python3 -c '
import json, sys
try:
    networks = json.load(sys.stdin)
    for cfg in networks[0].get("IPAM", {}).get("Config", []):
        subnet = cfg.get("Subnet", "")
        if subnet and ":" not in subnet:
            print(subnet)
            break
except Exception:
    pass
' || true)
if [[ -z "$KIND_SUBNET" || "$KIND_SUBNET" == "<no value>" ]]; then
  KIND_SUBNET="172.21.0.0/16"
fi

if [[ -z "$WINDOWS_POD_BLOCK" ]]; then
  WINDOWS_POD_BLOCK=$(kubectl --kubeconfig "$KUBECONFIG" get blockaffinities.crd.projectcalico.org \
    -o jsonpath="{range .items[?(@.spec.node==\"$WIN_NODE_NAME\")]}{.spec.cidr}{\"\\n\"}{end}" 2>/dev/null \
    | grep -v ':' | head -1 || true)
fi
if [[ -z "$WINDOWS_POD_BLOCK" ]]; then
  WINDOWS_POD_BLOCK="10.244.85.192/26"
fi

if [[ -z "$LINUX_POD_BLOCKS" ]]; then
  LINUX_POD_BLOCKS=$(kubectl --kubeconfig "$KUBECONFIG" get blockaffinities.crd.projectcalico.org \
    -o jsonpath="{range .items[?(@.spec.node!=\"$WIN_NODE_NAME\")]}{.spec.node}{\"=\"}{.spec.cidr}{\"\\n\"}{end}" 2>/dev/null \
    | grep -v ':' || true)
fi
if [[ -z "$LINUX_POD_BLOCKS" ]]; then
  LINUX_POD_BLOCKS=$'kind-worker2=10.244.110.128/26\nkind-worker=10.244.162.128/26\nkind-control-plane=10.244.82.0/26'
fi

ipt_accept() {
  local chain="$1" position="$2"
  shift 2
  sudo iptables -C "$chain" "$@" 2>/dev/null || sudo iptables -I "$chain" "$position" "$@"
}

nat_return() {
  sudo iptables -t nat -C POSTROUTING "$@" 2>/dev/null || sudo iptables -t nat -I POSTROUTING 1 "$@"
}

echo "Applying kind/QEMU forwarding:"
echo "  kubeconfig:       $KUBECONFIG"
echo "  Windows node:     $WIN_NODE_NAME $WIN_NODE_IP"
echo "  host bridge:      $HOST_BRIDGE $HOST_BR0_IP"
echo "  kind bridge:      $KIND_BRIDGE $KIND_SUBNET"
echo "  pod CIDR:         $POD_CIDR"
echo "  Windows pod CIDR: $WINDOWS_POD_BLOCK"

sudo nft insert rule ip raw PREROUTING ip saddr "$WIN_NODE_IP" ip daddr "$KIND_SUBNET" accept 2>/dev/null || true
sudo nft insert rule ip raw PREROUTING ip saddr "$KIND_SUBNET" ip daddr "$WIN_NODE_IP" accept 2>/dev/null || true

ipt_accept DOCKER-USER 1 -s "$WIN_NODE_IP/32" -d "$KIND_SUBNET" -j ACCEPT
ipt_accept DOCKER-USER 1 -s "$KIND_SUBNET" -d "$WIN_NODE_IP/32" -j ACCEPT
ipt_accept DOCKER-USER 1 -s "$WINDOWS_POD_BLOCK" -d "$POD_CIDR" -j ACCEPT
ipt_accept DOCKER-USER 1 -s "$POD_CIDR" -d "$WINDOWS_POD_BLOCK" -j ACCEPT
ipt_accept DOCKER-USER 1 -s "$WIN_NODE_IP/32" -d "$POD_CIDR" -j ACCEPT
ipt_accept DOCKER-USER 1 -s "$POD_CIDR" -d "$WIN_NODE_IP/32" -j ACCEPT

nat_return -s "$KIND_SUBNET" -d "$WIN_NODE_IP/32" -j RETURN
nat_return -s "$POD_CIDR" -d "$WINDOWS_POD_BLOCK" -j RETURN
nat_return -s "$WINDOWS_POD_BLOCK" -d "$POD_CIDR" -j RETURN
nat_return -s "$POD_CIDR" -d "$WIN_NODE_IP/32" -j RETURN
nat_return -s "$WIN_NODE_IP/32" -d "$POD_CIDR" -j RETURN

sudo ip route replace "$WINDOWS_POD_BLOCK" via "$WIN_NODE_IP" dev "$HOST_BRIDGE"

windows_routes=("$KIND_SUBNET")
while IFS= read -r item; do
  [[ -z "$item" ]] && continue
  node="${item%%=*}"
  block="${item#*=}"
  node_ip=$(kubectl --kubeconfig "$KUBECONFIG" get node "$node" \
    -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)
  if [[ -n "$node_ip" && -n "$block" ]]; then
    echo "  Linux pod route: $block via $node_ip dev $KIND_BRIDGE"
    sudo ip route replace "$block" via "$node_ip" dev "$KIND_BRIDGE"
    windows_routes+=("$block")
  fi
done <<< "$LINUX_POD_BLOCKS"

route_array=""
for route in "${windows_routes[@]}"; do
  route_array+="\"$route\","
done
route_array="${route_array%,}"

tmp_ps=$(mktemp)
trap 'rm -f "$tmp_ps"' EXIT
cat > "$tmp_ps" <<EOF
\$if = (Get-NetIPInterface -InterfaceAlias 'vEthernet (Ethernet 2)' -AddressFamily IPv4 -ErrorAction SilentlyContinue).InterfaceIndex
if (-not \$if) { \$if = (Get-NetIPInterface -InterfaceAlias 'Ethernet 2' -AddressFamily IPv4).InterfaceIndex }
if (-not \$if) { throw 'Unable to find Windows cluster NIC interface index' }
foreach (\$prefix in @($route_array)) {
  Remove-NetRoute -DestinationPrefix \$prefix -Confirm:\$false -ErrorAction SilentlyContinue
  New-NetRoute -DestinationPrefix \$prefix -NextHop '$HOST_BR0_IP' -InterfaceIndex \$if -PolicyStore ActiveStore -RouteMetric 5 | Out-Null
}
Get-NetRoute -AddressFamily IPv4 | Where-Object { @($route_array) -contains \$_.DestinationPrefix } |
  Sort-Object DestinationPrefix | Format-Table -AutoSize
EOF

scp -o StrictHostKeyChecking=no "$tmp_ps" "$WIN_SSH_USER@$WIN_NODE_IP:C:/Windows/Temp/apply-kind-qemu-forwarding.ps1" >/dev/null
ssh -o StrictHostKeyChecking=no "$WIN_SSH_USER@$WIN_NODE_IP" \
  'powershell -NoProfile -ExecutionPolicy Bypass -File C:/Windows/Temp/apply-kind-qemu-forwarding.ps1'

echo "kind/QEMU forwarding applied."
