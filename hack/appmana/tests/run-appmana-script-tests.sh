#!/bin/bash
# Mocked tests for AppMana kind/QEMU lab scripts.

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../../.." && pwd)

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

MOCKBIN="$TMPDIR/bin"
LOG="$TMPDIR/commands.log"
mkdir -p "$MOCKBIN"
: > "$LOG"

write_mock() {
  local name="$1"
  shift
  cat > "$MOCKBIN/$name"
  chmod +x "$MOCKBIN/$name"
}

write_mock kubectl <<'EOF'
#!/bin/bash
set -euo pipefail
echo "kubectl $*" >> "$APP_MOCK_LOG"

args=("$@")
if [[ "${args[0]:-}" == "--kubeconfig" ]]; then
  args=("${args[@]:2}")
fi

if [[ "${args[0]:-}" == "get" && "${args[1]:-}" == "node" ]]; then
  node="${args[2]}"
  joined=" ${args[*]} "
  if [[ "$joined" == *"kubernetes\\.io/os"* ]]; then
    [[ "$node" == "appmana-000" ]] && echo -n windows || echo -n linux
    exit 0
  fi
  if [[ "$joined" == *"InternalIP"* ]]; then
    case "$node" in
      appmana-000) echo -n 10.2.0.180 ;;
      kind-worker2) echo -n 172.21.0.2 ;;
      kind-worker) echo -n 172.21.0.3 ;;
      kind-control-plane) echo -n 172.21.0.4 ;;
    esac
    exit 0
  fi
fi

if [[ "${args[0]:-}" == "get" && "${args[1]:-}" == "blockaffinities.crd.projectcalico.org" ]]; then
  joined=" ${args[*]} "
  if [[ "$joined" == *'@.spec.node=="appmana-000"'* ]]; then
    printf '10.244.85.192/26\n'
  else
    printf 'kind-worker2=10.244.110.128/26\nkind-worker=10.244.162.128/26\nkind-control-plane=10.244.82.0/26\n'
  fi
  exit 0
fi

if [[ "${args[0]:-}" == "run" || "${args[2]:-}" == "run" ]]; then
  pod="${args[1]:-${args[3]}}"
  echo "pod/$pod created"
  exit 0
fi

if [[ "${args[0]:-}" == "wait" || "${args[2]:-}" == "wait" ]]; then
  echo "pod condition met"
  exit 0
fi

if [[ "${args[0]:-}" == "expose" || "${args[2]:-}" == "expose" ]]; then
  name=""
  for a in "${args[@]}"; do
    [[ "$a" == --name=* ]] && name="${a#--name=}"
  done
  echo "service/$name exposed"
  exit 0
fi

if [[ "${args[0]:-}" == "create" && "${args[1]:-}" == "namespace" ]]; then
  echo 'apiVersion: v1'
  echo 'kind: Namespace'
  echo "metadata: {name: ${args[2]}}"
  exit 0
fi

if [[ "${args[0]:-}" == "apply" ]]; then
  cat >/dev/null
  echo "namespace/calico-qemu-test configured"
  exit 0
fi

if [[ "${args[0]:-}" == "delete" ]]; then
  exit 0
fi

if [[ "${args[0]:-}" == "get" && "${args[1]:-}" == "pod" ]]; then
  pod="${args[2]}"
  joined=" ${args[*]} "
  if [[ "$joined" == *".status.phase"* ]]; then echo -n Running; exit 0; fi
  if [[ "$joined" == *".status.podIPs[*].ip"* ]]; then
    [[ "$pod" == "hc-appmana-000" ]] && echo -n 10.244.85.222 || echo -n 10.244.110.172
    exit 0
  fi
  if [[ "$joined" == *".status.podIPs[0].ip"* ]]; then
    [[ "$pod" == "hc-appmana-000" ]] && echo -n 10.244.85.222 || echo -n 10.244.110.172
    exit 0
  fi
  if [[ "$joined" == *".status.podIPs[1].ip"* ]]; then exit 0; fi
  if [[ "$joined" == *"containerStatuses[0].containerID"* ]]; then
    [[ "$pod" == "hc-appmana-000" ]] && echo -n containerd://wincid || echo -n containerd://linuxcid
    exit 0
  fi
fi

if [[ "${args[0]:-}" == "get" && "${args[1]:-}" == "service" ]]; then
  svc="${args[2]}"
  [[ "$svc" == "svc-hc-appmana-000-v4" ]] && echo -n 10.96.174.51 || echo -n 10.96.149.10
  exit 0
fi

if [[ "${args[0]:-}" == "exec" || "${args[2]:-}" == "exec" ]]; then
  joined=" ${args[*]} "
  if [[ "$joined" == *"ping"* ]]; then echo "64 bytes from target"; exit 0; fi
  if [[ "$joined" == *"wget"* ]]; then echo "ok"; exit 0; fi
  if [[ "$joined" == *"curl"* ]]; then exit 0; fi
fi

echo "unhandled kubectl $*" >&2
exit 1
EOF

write_mock docker <<'EOF'
#!/bin/bash
set -euo pipefail
echo "docker $*" >> "$APP_MOCK_LOG"
if [[ "$*" == *'com.docker.network.bridge.name'* ]]; then echo -n br-64a19c4dd412; exit 0; fi
if [[ "$*" == *'.Id'* ]]; then echo -n 64a19c4dd412abcdef; exit 0; fi
if [[ "$*" == "network inspect kind" ]]; then
  printf '[{"IPAM":{"Config":[{"Subnet":"fc00:f853:ccd:e793::/64"},{"Subnet":"172.21.0.0/16"}]}}]'
  exit 0
fi
exit 0
EOF

write_mock ip <<'EOF'
#!/bin/bash
set -euo pipefail
echo "ip $*" >> "$APP_MOCK_LOG"
if [[ "$1" == "-4" ]]; then echo "1: br0 inet 10.2.0.55/24 brd 10.2.0.255 scope global br0"; exit 0; fi
exit 0
EOF

write_mock sudo <<'EOF'
#!/bin/bash
set -euo pipefail
echo "sudo $*" >> "$APP_MOCK_LOG"
if [[ "${1:-}" == "iptables" && "${2:-}" == "-C" ]]; then exit 1; fi
if [[ "${1:-}" == "iptables" && "${2:-}" == "-t" && "${4:-}" == "-C" ]]; then exit 1; fi
exit 0
EOF

write_mock nft <<'EOF'
#!/bin/bash
echo "nft $*" >> "$APP_MOCK_LOG"
exit 0
EOF

write_mock ssh <<'EOF'
#!/bin/bash
echo "ssh $*" >> "$APP_MOCK_LOG"
if [[ "$*" == *"hcsdiag exec wincid"* ]]; then exit 0; fi
if [[ "$*" == *"powershell"* ]]; then
  cp "$APP_WINDOWS_ROUTE_SCRIPT" "$APP_WINDOWS_ROUTE_CAPTURE"
  echo "Windows route applied"
  exit 0
fi
exit 0
EOF

write_mock scp <<'EOF'
#!/bin/bash
echo "scp $*" >> "$APP_MOCK_LOG"
src=""
for a in "$@"; do
  if [[ -f "$a" ]]; then
    src="$a"
    break
  fi
done
cp "$src" "$APP_WINDOWS_ROUTE_SCRIPT"
exit 0
EOF

write_mock iconv <<'EOF'
#!/bin/bash
cat
EOF

write_mock base64 <<'EOF'
#!/bin/bash
cat >/dev/null
echo encoded
EOF

write_mock ping <<'EOF'
#!/bin/bash
echo "64 bytes from $4"
exit 0
EOF

write_mock ping6 <<'EOF'
#!/bin/bash
echo "64 bytes from $4"
exit 0
EOF

assert_log_contains() {
  local pattern="$1"
  if ! grep -Fq -- "$pattern" "$LOG"; then
    echo "Expected log to contain: $pattern" >&2
    echo "--- command log ---" >&2
    cat "$LOG" >&2
    exit 1
  fi
}

PATH="$MOCKBIN:$PATH"
export PATH APP_MOCK_LOG="$LOG" KUBECONFIG="$TMPDIR/kubeconfig"
export APP_WINDOWS_ROUTE_SCRIPT="$TMPDIR/windows-route-script.ps1"
export APP_WINDOWS_ROUTE_CAPTURE="$TMPDIR/windows-route-capture.ps1"
touch "$KUBECONFIG"

bash "$REPO_ROOT/hack/appmana/apply-kind-qemu-forwarding.sh" >"$TMPDIR/apply-kind-qemu-forwarding.out"
assert_log_contains "sudo nft insert rule ip raw PREROUTING ip saddr 10.2.0.180 ip daddr 172.21.0.0/16 accept"
assert_log_contains "sudo iptables -I DOCKER-USER 1 -s 10.2.0.180/32 -d 172.21.0.0/16 -j ACCEPT"
assert_log_contains "sudo iptables -I DOCKER-USER 1 -s 172.21.0.0/16 -d 10.2.0.180/32 -j ACCEPT"
assert_log_contains "sudo iptables -I DOCKER-USER 1 -s 10.244.85.192/26 -d 10.244.0.0/16 -j ACCEPT"
assert_log_contains "sudo iptables -I DOCKER-USER 1 -s 10.2.0.180/32 -d 10.244.0.0/16 -j ACCEPT"
assert_log_contains "sudo iptables -t nat -I POSTROUTING 1 -s 10.244.0.0/16 -d 10.244.85.192/26 -j RETURN"
assert_log_contains "sudo iptables -t nat -I POSTROUTING 1 -s 10.2.0.180/32 -d 10.244.0.0/16 -j RETURN"
assert_log_contains "sudo ip route replace 10.244.85.192/26 via 10.2.0.180 dev br0"
assert_log_contains "ssh -o StrictHostKeyChecking=no administrator@10.2.0.180 powershell"
grep -Fq '"172.21.0.0/16","10.244.110.128/26","10.244.162.128/26","10.244.82.0/26"' "$APP_WINDOWS_ROUTE_CAPTURE"
grep -Fq "New-NetRoute -DestinationPrefix \$prefix -NextHop '10.2.0.55'" "$APP_WINDOWS_ROUTE_CAPTURE"

: > "$LOG"
bash "$REPO_ROOT/hack/appmana/ipv6-health-check.sh" \
  --namespace calico-qemu-test \
  --ipv4-pool kind-ipv4-pool \
  --ipv4-only \
  --skip-inbound \
  --windows-exec hcsdiag \
  --linux-image nicolaka/netshoot:latest \
  --win-image mcr.microsoft.com/windows/servercore:ltsc2022 \
  kind-worker2 appmana-000 >"$TMPDIR/ipv6-health-check.out"
grep -Fq "Total: 10  Pass: 10  Fail: 0" "$TMPDIR/ipv6-health-check.out"
grep -Fq "kind-worker2(linux) -> appmana-000(windows) Service IPv4" "$TMPDIR/ipv6-health-check.out"
grep -Fq "appmana-000(windows) -> kind-worker2(linux) Service IPv4" "$TMPDIR/ipv6-health-check.out"
grep -Fq "kind-worker2(linux) -> appmana-000 IPv4" "$TMPDIR/ipv6-health-check.out"
grep -Fq "appmana-000(windows) -> kind-worker2 IPv4" "$TMPDIR/ipv6-health-check.out"
grep -Fq "appmana-000 -> https://1.1.1.1 (IPv4 WAN TCP): PASS" "$TMPDIR/ipv6-health-check.out"
grep -Fq "ALL TESTS PASSED" "$TMPDIR/ipv6-health-check.out"
assert_log_contains "ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 administrator@10.2.0.180 hcsdiag exec wincid"

: > "$LOG"
HEALTH_CHECK_SCRIPT="$TMPDIR/mock-health.sh"
FORWARDING_SCRIPT="$TMPDIR/mock-forwarding.sh"
cat > "$HEALTH_CHECK_SCRIPT" <<'EOF'
#!/bin/bash
echo "health $*" >> "$APP_MOCK_LOG"
exit 0
EOF
cat > "$FORWARDING_SCRIPT" <<'EOF'
#!/bin/bash
echo "forwarding" >> "$APP_MOCK_LOG"
exit 0
EOF
chmod +x "$HEALTH_CHECK_SCRIPT" "$FORWARDING_SCRIPT"
HEALTH_CHECK_SCRIPT="$HEALTH_CHECK_SCRIPT" FORWARDING_SCRIPT="$FORWARDING_SCRIPT" \
  bash "$REPO_ROOT/hack/appmana/run-kind-qemu-health.sh" >"$TMPDIR/run-kind-qemu-health.out"
assert_log_contains "forwarding"
assert_log_contains "health --namespace calico-qemu-test --ipv4-pool kind-ipv4-pool --ipv4-only --windows-exec hcsdiag"

echo "AppMana script tests passed."
