#!/bin/bash
# IPv6 Dual-Stack Health Check
# Tests IPv4 and IPv6 reachability for Linux and Windows pods, services, and
# WAN egress. Runs source {Linux pod, Windows pod} against destinations
# {Linux pod, Windows pod, Linux service, Windows service, WAN}.
#
# Usage:
#   ./ipv6-health-check.sh [--namespace NS] [--service-account SA] [--ipv4-only] [--service-only] NODE1 [NODE2 ...]
#
# Examples:
#   ./ipv6-health-check.sh appmana-003
#   ./ipv6-health-check.sh appmana-003 appmana-007
#   ./ipv6-health-check.sh appmana-003 appmana-007 appmana-009

set -uo pipefail

NAMESPACE="appmana"
SERVICE_ACCOUNT="default"
IPV4_POOL=""
IPV6_POOL=""
WIN_IMAGE="mcr.microsoft.com/windows/servercore:ltsc2022"
LINUX_IMAGE="nicolaka/netshoot:latest"
IMAGE_PULL_SECRET="${IMAGE_PULL_SECRET:-}"
WINDOWS_EXEC="kubectl"
WINDOWS_SSH_USER="administrator"
IPV4_ONLY=false
SKIP_EXTERNAL=false
SKIP_INBOUND=false
SERVICE_ONLY=false
SERVICE_PORT=8080
NODES=()

while [[ $# -gt 0 ]]; do
  case $1 in
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --service-account) SERVICE_ACCOUNT="$2"; shift 2 ;;
    --ipv4-pool) IPV4_POOL="$2"; shift 2 ;;
    --ipv6-pool) IPV6_POOL="$2"; shift 2 ;;
    --win-image) WIN_IMAGE="$2"; shift 2 ;;
    --image-pull-secret) IMAGE_PULL_SECRET="$2"; shift 2 ;;
    --linux-image) LINUX_IMAGE="$2"; shift 2 ;;
    --windows-exec) WINDOWS_EXEC="$2"; shift 2 ;;
    --windows-ssh-user) WINDOWS_SSH_USER="$2"; shift 2 ;;
    --ipv4-only) IPV4_ONLY=true; shift ;;
    --skip-external) SKIP_EXTERNAL=true; shift ;;
    --skip-inbound) SKIP_INBOUND=true; shift ;;
    --service-only) SERVICE_ONLY=true; SKIP_EXTERNAL=true; SKIP_INBOUND=true; shift ;;
    --service-port) SERVICE_PORT="$2"; shift 2 ;;
    *) NODES+=("$1"); shift ;;
  esac
done

if [[ ${#NODES[@]} -eq 0 ]]; then
  echo "Usage: $0 [options] NODE1 [NODE2 ...]"
  exit 1
fi

# Auto-detect pools if not specified
if [[ -z "$IPV4_POOL" ]]; then
  IPV4_POOL=$(kubectl get ippool -o json | python3 -c "
import json,sys
for p in json.load(sys.stdin)['items']:
  if not p['spec'].get('disabled') and ':' not in p['spec']['cidr']:
    print(p['metadata']['name']); break
" 2>/dev/null || true)
fi
if [[ "$IPV4_ONLY" != "true" && -z "$IPV6_POOL" ]]; then
  IPV6_POOL=$(kubectl get ippool -o json | python3 -c "
import json,sys
for p in json.load(sys.stdin)['items']:
  if not p['spec'].get('disabled') and ':' in p['spec']['cidr']:
    print(p['metadata']['name']); break
" 2>/dev/null || true)
fi
if [[ "$IPV4_ONLY" == "true" ]]; then
  IPV6_POOL=""
fi

ANNOTATIONS=""
if [[ -n "$IPV4_POOL" ]]; then
  ANNOTATIONS="\"cni.projectcalico.org/ipv4pools\": \"[\\\"$IPV4_POOL\\\"]\""
fi
if [[ -n "$IPV6_POOL" ]]; then
  if [[ -n "$ANNOTATIONS" ]]; then ANNOTATIONS="$ANNOTATIONS, "; fi
  ANNOTATIONS="${ANNOTATIONS}\"cni.projectcalico.org/ipv6pools\": \"[\\\"$IPV6_POOL\\\"]\""
fi

CLEANUP_PODS=()
CLEANUP_SERVICES=()
cleanup() {
  echo ""
  echo "Cleaning up..."
  for svcname in "${CLEANUP_SERVICES[@]}"; do
    kubectl delete service "$svcname" -n "$NAMESPACE" 2>/dev/null || true
  done
  for podname in "${CLEANUP_PODS[@]}"; do
    kubectl delete pod "$podname" -n "$NAMESPACE" --force --grace-period=0 2>/dev/null || true
  done
}
trap cleanup EXIT INT TERM

first_ipv4() {
  local value
  for value in "$@"; do
    if [[ "$value" != *:* && "$value" =~ ^[0-9]+(\.[0-9]+){3}$ ]]; then
      printf '%s\n' "$value"
      return 0
    fi
  done
  return 1
}

declare -A NODE_OS
declare -A NODE_IP
for node in "${NODES[@]}"; do
  os=$(kubectl get node "$node" -o jsonpath='{.metadata.labels.kubernetes\.io/os}' 2>/dev/null)
  if [[ -z "$os" ]]; then
    echo "ERROR: node $node not found or has no kubernetes.io/os label"
    exit 1
  fi
  NODE_OS[$node]="$os"
  node_ips=$(kubectl get node "$node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null)
  NODE_IP[$node]=$(first_ipv4 $node_ips || true)
done

if [[ "$WINDOWS_EXEC" != "kubectl" && "$WINDOWS_EXEC" != "hcsdiag" ]]; then
  echo "ERROR: --windows-exec must be kubectl or hcsdiag"
  exit 1
fi

echo ""
echo "=== IPv6 Dual-Stack Health Check ==="
echo "Namespace: $NAMESPACE"
echo "Nodes:"
for node in "${NODES[@]}"; do echo "  $node (${NODE_OS[$node]})"; done
echo "IPv4 Pool: ${IPV4_POOL:-none}"
echo "IPv6 Pool: ${IPV6_POOL:-none}"
echo ""

declare -A POD_IPV4
declare -A POD_IPV6
declare -A POD_NAME
declare -A POD_CONTAINER_ID
declare -A SERVICE_IPV4
declare -A SERVICE_IPV6
SETUP_FAILED=false

http_url() {
  local ip="$1" port="$2"
  if [[ "$ip" == *:* ]]; then
    printf 'http://[%s]:%s/' "$ip" "$port"
  else
    printf 'http://%s:%s/' "$ip" "$port"
  fi
}

route_diag() {
  local target="$1" fam="$2"
  if [[ "$fam" == "v6" ]]; then
    if command -v ip >/dev/null 2>&1; then
      ip -6 route get "$target" 2>&1 | sed 's/^/    route: /' || true
    elif command -v route >/dev/null 2>&1; then
      route print -6 "$target" 2>&1 | sed 's/^/    route: /' || true
    fi
  else
    if command -v ip >/dev/null 2>&1; then
      ip route get "$target" 2>&1 | sed 's/^/    route: /' || true
    elif command -v route >/dev/null 2>&1; then
      route print "$target" 2>&1 | sed 's/^/    route: /' || true
    fi
  fi
}

# Per-OS pod spec
podspec() {
  local node="$1" os="$2" podname="$3"
  if [[ "$os" == "windows" ]]; then
    local pull_secret=""
    if [[ -n "$IMAGE_PULL_SECRET" ]]; then
      pull_secret="\"imagePullSecrets\": [{\"name\": \"$IMAGE_PULL_SECRET\"}],"
    fi
    cat <<EOF
{
  "metadata": {
    "labels": {"app": "$podname"},
    "annotations": {$ANNOTATIONS}
  },
  "spec": {
    "nodeSelector": {"kubernetes.io/hostname": "$node"},
    "tolerations": [{"operator": "Exists"}],
    "serviceAccountName": "$SERVICE_ACCOUNT",
    $pull_secret
    "containers": [{
      "name": "test",
      "image": "$WIN_IMAGE",
      "command": ["powershell","-NoProfile","-Command","\$listener = [System.Net.HttpListener]::new(); \$listener.Prefixes.Add('http://+:$SERVICE_PORT/'); \$listener.Start(); while (\$true) { \$ctx = \$listener.GetContext(); \$bytes = [Text.Encoding]::ASCII.GetBytes('ok'); \$ctx.Response.StatusCode = 200; \$ctx.Response.OutputStream.Write(\$bytes, 0, \$bytes.Length); \$ctx.Response.Close() }"],
      "ports": [{"containerPort": $SERVICE_PORT}]
    }]
  }
}
EOF
  else
    cat <<EOF
{
  "metadata": {
    "labels": {"app": "$podname"},
    "annotations": {$ANNOTATIONS}
  },
  "spec": {
    "nodeSelector": {"kubernetes.io/hostname": "$node"},
    "serviceAccountName": "$SERVICE_ACCOUNT",
    "containers": [{
      "name": "test",
      "image": "$LINUX_IMAGE",
      "command": ["sh","-c","mkdir -p /tmp/www; printf ok > /tmp/www/index.html; httpd -f -p $SERVICE_PORT -h /tmp/www"],
      "ports": [{"containerPort": $SERVICE_PORT}]
    }]
  }
}
EOF
  fi
}

for node in "${NODES[@]}"; do
  podname="hc-${node}"
  CLEANUP_PODS+=("$podname")
  POD_NAME[$node]="$podname"
  os="${NODE_OS[$node]}"
  image="$WIN_IMAGE"
  [[ "$os" == "linux" ]] && image="$LINUX_IMAGE"
  if ! kubectl run "$podname" --image="$image" --restart=Never -n "$NAMESPACE" \
    --overrides="$(podspec "$node" "$os" "$podname")"; then
    echo "ERROR: failed to create pod $podname on $node ($os)"
    exit 1
  fi
  echo "Created pod $podname on $node ($os)"
done

echo ""
echo "Waiting for ALL pods to be Running (up to 600s)..."
WAIT_START=$(date +%s)
WAIT_TIMEOUT=600
while true; do
  elapsed=$(( $(date +%s) - WAIT_START ))
  if [[ $elapsed -ge $WAIT_TIMEOUT ]]; then
    echo "ERROR: Timed out after ${WAIT_TIMEOUT}s waiting for pods."
    for node in "${NODES[@]}"; do
      podname="hc-${node}"
      phase=$(kubectl get pod "$podname" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null)
      reason=$(kubectl get pod "$podname" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}' 2>/dev/null)
      echo "  $podname: phase=$phase reason=${reason:-n/a}"
    done
    exit 1
  fi
  all_ready=true
  for node in "${NODES[@]}"; do
    phase=$(kubectl get pod "hc-${node}" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null)
    if [[ -z "$phase" ]]; then
      echo "ERROR: pod hc-${node} disappeared while waiting."
      exit 1
    fi
    if [[ "$phase" == "Failed" ]]; then
      echo "ERROR: pod hc-${node} failed while waiting."
      kubectl logs "hc-${node}" -n "$NAMESPACE" 2>/dev/null || true
      exit 1
    fi
    if [[ "$phase" != "Running" ]]; then all_ready=false; break; fi
  done
  if $all_ready; then echo "All pods Running after ${elapsed}s."; break; fi
  if (( elapsed % 30 < 5 )); then echo "  ${elapsed}s: waiting..."; fi
  sleep 5
done

for node in "${NODES[@]}"; do
  podname="hc-${node}"
  pod_ips=$(kubectl get pod "$podname" -n "$NAMESPACE" -o jsonpath='{.status.podIPs[*].ip}' 2>/dev/null)
  ipv4=""
  ipv6=""
  for ipaddr in $pod_ips; do
    if [[ "$ipaddr" == *:* ]]; then
      [[ -z "$ipv6" ]] && ipv6="$ipaddr"
    else
      [[ -z "$ipv4" ]] && ipv4="$ipaddr"
    fi
  done
  POD_IPV4[$node]="$ipv4"
  POD_IPV6[$node]="$ipv6"
  cid=$(kubectl get pod "$podname" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].containerID}' 2>/dev/null)
  POD_CONTAINER_ID[$node]="${cid#containerd://}"
  echo "$node (${NODE_OS[$node]}): IPv4=$ipv4 IPv6=$ipv6"
  if [[ -z "$ipv4" ]]; then
    echo "  ERROR: No IPv4"
    SETUP_FAILED=true
  fi
  if [[ "$IPV4_ONLY" != "true" && -n "$IPV6_POOL" && -z "$ipv6" ]]; then
    echo "  ERROR: No IPv6"
    SETUP_FAILED=true
  fi
done

if [[ "$SETUP_FAILED" == "true" ]]; then
  echo "ERROR: pod address setup did not match requested IP families"
  exit 1
fi

for node in "${NODES[@]}"; do
  podname="${POD_NAME[$node]}"
  svcname4="svc-${podname}-v4"
  CLEANUP_SERVICES+=("$svcname4")
  if ! kubectl apply -n "$NAMESPACE" -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata:
  name: $svcname4
spec:
  ipFamilyPolicy: SingleStack
  ipFamilies:
    - IPv4
  selector:
    app: $podname
  ports:
    - name: http
      port: $SERVICE_PORT
      targetPort: $SERVICE_PORT
EOF
  then
    echo "ERROR: failed to create IPv4 service $svcname4"
    exit 1
  fi
  SERVICE_IPV4[$node]=$(kubectl get service "$svcname4" -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}')
  if [[ -z "${SERVICE_IPV4[$node]}" ]]; then
    echo "ERROR: IPv4 service $svcname4 has no ClusterIP"
    exit 1
  fi
  echo "$node (${NODE_OS[$node]}): Service IPv4=${SERVICE_IPV4[$node]}:$SERVICE_PORT"

  if [[ -n "$IPV6_POOL" ]]; then
    svcname6="svc-${podname}-v6"
    CLEANUP_SERVICES+=("$svcname6")
    if ! kubectl apply -n "$NAMESPACE" -f - >/dev/null <<EOF
apiVersion: v1
kind: Service
metadata:
  name: $svcname6
spec:
  ipFamilyPolicy: SingleStack
  ipFamilies:
    - IPv6
  selector:
    app: $podname
  ports:
    - name: http
      port: $SERVICE_PORT
      targetPort: $SERVICE_PORT
EOF
    then
      echo "ERROR: failed to create IPv6 service $svcname6"
      exit 1
    fi
    SERVICE_IPV6[$node]=$(kubectl get service "$svcname6" -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}')
    if [[ -z "${SERVICE_IPV6[$node]}" ]]; then
      echo "ERROR: IPv6 service $svcname6 has no ClusterIP"
      exit 1
    fi
    echo "$node (${NODE_OS[$node]}): Service IPv6=${SERVICE_IPV6[$node]}:$SERVICE_PORT"
  fi
done

# Windows kube-proxy programs HNS load balancers asynchronously after the
# Service and EndpointSlice watches arrive. Give it one sync window before
# testing ClusterIP reachability.
sleep 5

# OS-specific ping helpers. Returns 0 on success, non-zero on failure.
# Args: node pod_name namespace target_ip family os
windows_hcsdiag_ps() {
  local node="$1" script="$2" node_ip="${NODE_IP[$node]:-}" cid="${POD_CONTAINER_ID[$node]:-}"
  if [[ -z "$node_ip" || -z "$cid" ]]; then
    return 1
  fi
  local encoded
  encoded=$(printf "%s" "$script" | iconv -f UTF-8 -t UTF-16LE | base64 -w0)
  ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 \
    "${WINDOWS_SSH_USER}@${node_ip}" \
    "hcsdiag exec $cid powershell -NoProfile -EncodedCommand $encoded" >/dev/null 2>&1
}

ping_from() {
  local node="$1" pod="$2" ns="$3" tgt="$4" fam="$5" os="$6"
  if [[ "$os" == "windows" ]]; then
    if [[ "$WINDOWS_EXEC" == "hcsdiag" ]]; then
      if [[ "$fam" == "v6" ]]; then
        windows_hcsdiag_ps "$node" "if (Test-Connection -IPv6 -Count 1 -Quiet '$tgt') { exit 0 } else { exit 1 }"
      else
        windows_hcsdiag_ps "$node" "if (Test-Connection -Count 1 -Quiet '$tgt') { exit 0 } else { exit 1 }"
      fi
    else
      if [[ "$fam" == "v6" ]]; then
        kubectl exec "$pod" -n "$ns" -- cmd /c "ping -6 -n 1 -w 3000 $tgt" 2>&1 | grep -qE "Reply from|bytes=32 time"
      else
        kubectl exec "$pod" -n "$ns" -- cmd /c "ping -n 1 -w 3000 $tgt" 2>&1 | grep -qE "Reply from|bytes=32 time"
      fi
    fi
  else
    if [[ "$fam" == "v6" ]]; then
      kubectl exec "$pod" -n "$ns" -- ping -6 -c 1 -W 3 "$tgt" 2>&1 | grep -q "bytes from"
    else
      kubectl exec "$pod" -n "$ns" -- ping -c 1 -W 3 "$tgt" 2>&1 | grep -q "bytes from"
    fi
  fi
}

http_from() {
  local node="$1" pod="$2" ns="$3" url="$4" os="$5"
  if [[ "$os" == "windows" ]]; then
    if [[ "$WINDOWS_EXEC" == "hcsdiag" ]]; then
      windows_hcsdiag_ps "$node" "\$r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 '$url'; \$c = \$r.Content; if (\$c -is [byte[]]) { \$c = [Text.Encoding]::ASCII.GetString(\$c) }; if (\$c -match 'ok') { exit 0 } else { exit 1 }"
    else
      kubectl exec "$pod" -n "$ns" -- cmd /c "curl.exe -s --connect-timeout 3 --max-time 5 $url" 2>&1 | grep -q "ok"
    fi
  else
    kubectl exec "$pod" -n "$ns" -- wget -q -T 5 -O - "$url" 2>&1 | grep -q "ok"
  fi
}

wan_from() {
  local node="$1" pod="$2" ns="$3" fam="$4" os="$5" url
  if [[ "$fam" == "v6" ]]; then
    url="https://[2606:4700:4700::1111]/"
  else
    url="https://1.1.1.1/"
  fi

  if [[ "$os" == "windows" ]]; then
    if [[ "$WINDOWS_EXEC" == "hcsdiag" ]]; then
      if [[ "$fam" == "v6" ]]; then
        windows_hcsdiag_ps "$node" "if ((Test-NetConnection '2606:4700:4700::1111' -Port 443).TcpTestSucceeded) { exit 0 } else { exit 1 }"
      else
        windows_hcsdiag_ps "$node" "if ((Test-NetConnection '1.1.1.1' -Port 443).TcpTestSucceeded) { exit 0 } else { exit 1 }"
      fi
    else
      kubectl exec "$pod" -n "$ns" -- cmd /c "curl.exe -fsSk --ssl-no-revoke --connect-timeout 5 --max-time 10 $url -o NUL" >/dev/null 2>&1
    fi
  else
    kubectl exec "$pod" -n "$ns" -- sh -c "curl -fsSk --connect-timeout 5 --max-time 10 '$url' -o /dev/null" >/dev/null 2>&1
  fi
}

PASS=0
FAIL=0
TOTAL=0

if [[ "$SKIP_EXTERNAL" != "true" ]]; then
  echo ""
  echo "=== External Reachability ==="
  for node in "${NODES[@]}"; do
    podname="hc-${node}"; os="${NODE_OS[$node]}"
    TOTAL=$((TOTAL+1))
    if wan_from "$node" "$podname" "$NAMESPACE" "v4" "$os"; then
      echo "$node -> https://1.1.1.1 (IPv4 WAN TCP): PASS"; PASS=$((PASS+1))
    else
      echo "$node -> https://1.1.1.1 (IPv4 WAN TCP): FAIL"; FAIL=$((FAIL+1))
    fi
    if [[ -n "$IPV6_POOL" && -n "${POD_IPV6[$node]:-}" ]]; then
      TOTAL=$((TOTAL+1))
      if wan_from "$node" "$podname" "$NAMESPACE" "v6" "$os"; then
        echo "$node -> https://[2606:4700:4700::1111] (IPv6 WAN TCP): PASS"; PASS=$((PASS+1))
      else
        echo "$node -> https://[2606:4700:4700::1111] (IPv6 WAN TCP): FAIL"; FAIL=$((FAIL+1))
      fi
    fi
  done
fi

if [[ "$SKIP_INBOUND" != "true" ]]; then
  echo ""
  echo "=== Inbound Reachability (from this host) ==="
  for node in "${NODES[@]}"; do
    ipv4="${POD_IPV4[$node]:-}"
    ipv6="${POD_IPV6[$node]:-}"
    if [[ -n "$ipv4" ]]; then
      TOTAL=$((TOTAL+1))
      if ping -c 1 -W 3 "$ipv4" 2>&1 | grep -q "bytes from"; then
        echo "-> $node IPv4 ($ipv4): PASS"; PASS=$((PASS+1))
      else
        echo "-> $node IPv4 ($ipv4): FAIL"; FAIL=$((FAIL+1))
        route_diag "$ipv4" "v4"
      fi
    fi
    if [[ -n "$IPV6_POOL" && -n "$ipv6" ]]; then
      TOTAL=$((TOTAL+1))
      if ping6 -c 1 -W 3 "$ipv6" 2>&1 | grep -q "bytes from"; then
        echo "-> $node IPv6 ($ipv6): PASS"; PASS=$((PASS+1))
      else
        echo "-> $node IPv6 ($ipv6): FAIL"; FAIL=$((FAIL+1))
        route_diag "$ipv6" "v6"
      fi
    fi
  done
fi

echo ""
echo "=== Service Reachability (${#NODES[@]} nodes, all services) ==="
for src_node in "${NODES[@]}"; do
  src_pod="${POD_NAME[$src_node]}"; src_os="${NODE_OS[$src_node]}"
  for dst_node in "${NODES[@]}"; do
    dst_os="${NODE_OS[$dst_node]}"
    dst_svc="${SERVICE_IPV4[$dst_node]:-}"
    if [[ -n "$dst_svc" ]]; then
      TOTAL=$((TOTAL+1))
      if http_from "$src_node" "$src_pod" "$NAMESPACE" "$(http_url "$dst_svc" "$SERVICE_PORT")" "$src_os"; then
        echo "$src_node($src_os) -> $dst_node($dst_os) Service IPv4 ($dst_svc:$SERVICE_PORT): PASS"; PASS=$((PASS+1))
      else
        echo "$src_node($src_os) -> $dst_node($dst_os) Service IPv4 ($dst_svc:$SERVICE_PORT): FAIL"; FAIL=$((FAIL+1))
      fi
    fi
    dst_svc="${SERVICE_IPV6[$dst_node]:-}"
    if [[ -n "$IPV6_POOL" && -n "$dst_svc" ]]; then
      TOTAL=$((TOTAL+1))
      if http_from "$src_node" "$src_pod" "$NAMESPACE" "$(http_url "$dst_svc" "$SERVICE_PORT")" "$src_os"; then
        echo "$src_node($src_os) -> $dst_node($dst_os) Service IPv6 ([$dst_svc]:$SERVICE_PORT): PASS"; PASS=$((PASS+1))
      else
        echo "$src_node($src_os) -> $dst_node($dst_os) Service IPv6 ([$dst_svc]:$SERVICE_PORT): FAIL"; FAIL=$((FAIL+1))
      fi
    fi
  done
done

if [[ "$SERVICE_ONLY" != "true" && ${#NODES[@]} -gt 1 ]]; then
  echo ""
  echo "=== Pod-to-Pod Reachability (${#NODES[@]} nodes, all pairs) ==="
  for src_node in "${NODES[@]}"; do
    src_pod="hc-${src_node}"; src_os="${NODE_OS[$src_node]}"
    for dst_node in "${NODES[@]}"; do
      dst_ipv4="${POD_IPV4[$dst_node]:-}"
      dst_ipv6="${POD_IPV6[$dst_node]:-}"
      if [[ -n "$dst_ipv4" ]]; then
        TOTAL=$((TOTAL+1))
        if http_from "$src_node" "$src_pod" "$NAMESPACE" "$(http_url "$dst_ipv4" "$SERVICE_PORT")" "$src_os"; then
          echo "$src_node($src_os) -> $dst_node IPv4 ($dst_ipv4): PASS"; PASS=$((PASS+1))
        else
          echo "$src_node($src_os) -> $dst_node IPv4 ($dst_ipv4): FAIL"; FAIL=$((FAIL+1))
        fi
      fi
      if [[ -n "$IPV6_POOL" && -n "$dst_ipv6" ]]; then
        TOTAL=$((TOTAL+1))
        if http_from "$src_node" "$src_pod" "$NAMESPACE" "$(http_url "$dst_ipv6" "$SERVICE_PORT")" "$src_os"; then
          echo "$src_node($src_os) -> $dst_node IPv6 ($dst_ipv6): PASS"; PASS=$((PASS+1))
        else
          echo "$src_node($src_os) -> $dst_node IPv6 ($dst_ipv6): FAIL"; FAIL=$((FAIL+1))
        fi
      fi
    done
  done
fi

echo ""
echo "=== Results ==="
echo "Total: $TOTAL  Pass: $PASS  Fail: $FAIL"
if [[ $FAIL -eq 0 && $TOTAL -gt 0 ]]; then
  echo "ALL TESTS PASSED"; exit 0
else
  echo "SOME TESTS FAILED"; exit 1
fi
