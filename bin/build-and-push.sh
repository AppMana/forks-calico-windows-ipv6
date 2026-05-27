#!/bin/bash
# Build calico-node Windows image and push to Harbor.
#
# Usage:
#   ./bin/build-and-push.sh                        # tags: v3.29.6-dualstack + SHA
#   ./bin/build-and-push.sh --tag custom-tag        # tags: custom-tag + SHA
#   ./bin/build-and-push.sh --no-push              # build only, don't push
#
# Prerequisites:
#   - Go 1.22+ (cross-compiles to windows/amd64)
#   - docker buildx with access to the Windows BuildKit service
#   - x86_64-w64-mingw32-gcc for the HNS IPv6 hook DLL
#   - BuildKit client certs (fetched automatically from K8s secret)
#
# The script:
#   1. Cross-compiles calico-node.exe, calico.exe, calico-ipam.exe, and HNS IPv6 artifacts
#   2. Copies confd scripts from the fork
#   3. Downloads nssm.exe and hns.psm1 if not cached
#   4. Builds the Windows container via remote BuildKit
#   5. Pushes to harbor.appmana.com/appmana-shared/node-windows

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

REGISTRY="harbor.appmana.com/appmana-shared"
IMAGE_NAME="node-windows"
BASE_TAG="v3.29.6-dualstack"
BUILDER="${BUILDER:-buildkit-windows}"
PUSH=true

while [[ $# -gt 0 ]]; do
  case $1 in
    --tag) BASE_TAG="$2"; shift 2 ;;
    --no-push) PUSH=false; shift ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

GIT_VERSION=$(git describe --tags --dirty --always --abbrev=12)
GIT_SHA=$(git rev-parse --short=12 HEAD)
GIT_REVISION=$(git rev-parse HEAD)

echo "=== Building calico-node Windows image ==="
echo "Commit:  $GIT_SHA ($GIT_VERSION)"
echo "Tags:    $BASE_TAG, $GIT_SHA"
echo "Builder: $BUILDER"
echo "Push:    $PUSH"
echo ""

# --- Step 1: Cross-compile Go binaries ---
echo "--- Compiling Go binaries (windows/amd64) ---"
mkdir -p node/dist/bin

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
  -o node/dist/bin/calico-node.exe \
  -buildvcs=false \
  -ldflags "-X node/buildinfo.GitVersion=$GIT_VERSION -X node/buildinfo.GitRevision=$GIT_REVISION" \
  ./node/cmd/calico-node/main.go

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
  -o node/dist/bin/calico.exe \
  -buildvcs=false \
  ./cni-plugin/cmd/calico/

cp node/dist/bin/calico.exe node/dist/bin/calico-ipam.exe

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
  -o node/dist/bin/hns-ipv6-injector.exe \
  -buildvcs=false \
  ./cni-plugin/cmd/hns-ipv6-injector

if ! command -v x86_64-w64-mingw32-gcc >/dev/null 2>&1; then
  echo "ERROR: x86_64-w64-mingw32-gcc is required to build hns-ipv6-hook.dll"
  echo "Install it with: sudo apt-get install -y gcc-mingw-w64-x86-64-win32"
  exit 1
fi
x86_64-w64-mingw32-gcc -shared \
  -o node/dist/bin/hns-ipv6-hook.dll \
  ./cni-plugin/cmd/hns-ipv6-hook/hook.c \
  -lws2_32 -liphlpapi -static-libgcc

echo "  calico-node.exe, calico.exe, calico-ipam.exe, hns-ipv6-injector.exe, hns-ipv6-hook.dll OK"

# --- Step 2: Copy confd scripts ---
echo "--- Copying confd scripts ---"
cp confd/windows-packaging/config-bgp.ps1     node/windows-packaging/CalicoWindows/confd/config-bgp.ps1
cp confd/windows-packaging/config-bgp.psm1    node/windows-packaging/CalicoWindows/confd/config-bgp.psm1
cp confd/windows-packaging/conf.d/blocks.toml node/windows-packaging/CalicoWindows/confd/conf.d/blocks.toml
cp confd/windows-packaging/conf.d/peerings.toml node/windows-packaging/CalicoWindows/confd/conf.d/peerings.toml
cp confd/windows-packaging/templates/blocks.ps1.template   node/windows-packaging/CalicoWindows/confd/templates/blocks.ps1.template
cp confd/windows-packaging/templates/peerings.ps1.template node/windows-packaging/CalicoWindows/confd/templates/peerings.ps1.template
echo "  confd files OK"

# --- Step 3: Download dependencies (cached) ---
echo "--- Checking dependencies ---"
NSSM_URL="https://storage.googleapis.com/public-calico-third-party-deps/nssm/nssm-2.24-103-gdee49fc.zip"
HNS_URL="https://raw.githubusercontent.com/microsoft/SDN/0d7593e5c8d4c2347079a7a6dbd9eb034ae19a44/Kubernetes/windows/hns.psm1"

if [[ ! -f node/windows-packaging/nssm.exe ]]; then
  echo "  Downloading nssm..."
  NSSM_TMP=$(mktemp -d)
  curl -sSL "$NSSM_URL" -o "$NSSM_TMP/nssm.zip"
  unzip -q "$NSSM_TMP/nssm.zip" -d "$NSSM_TMP"
  cp "$NSSM_TMP"/nssm-*/win64/nssm.exe node/windows-packaging/nssm.exe
  rm -rf "$NSSM_TMP"
  echo "  nssm.exe downloaded"
else
  echo "  nssm.exe cached"
fi

mkdir -p node/windows-packaging/CalicoWindows/libs/hns
if [[ ! -f node/windows-packaging/CalicoWindows/libs/hns/hns.psm1 ]]; then
  echo "  Downloading hns.psm1..."
  curl -sSL "$HNS_URL" -o node/windows-packaging/CalicoWindows/libs/hns/hns.psm1
  echo "  hns.psm1 downloaded"
else
  echo "  hns.psm1 cached"
fi

# --- Step 4: Fetch BuildKit certs ---
echo "--- Fetching BuildKit certs ---"
FETCH_SCRIPT="${REPO_ROOT}/../appmana/bin/fetch-buildkit-certs.sh"
if [[ ! -f "$FETCH_SCRIPT" ]]; then
  # Fallback: look in common locations
  FETCH_SCRIPT="$(find /home/administrator/Documents -name fetch-buildkit-certs.sh -maxdepth 4 2>/dev/null | head -1)"
fi
if [[ -z "$FETCH_SCRIPT" || ! -f "$FETCH_SCRIPT" ]]; then
  echo "ERROR: Cannot find fetch-buildkit-certs.sh"
  exit 1
fi
eval "$("$FETCH_SCRIPT")"
echo "  Certs in $BUILDKIT_CERTS_DIR"

# --- Step 5: Recreate remote Windows builder with fresh temp cert paths ---
echo "--- Creating $BUILDER builder ---"
docker buildx rm "$BUILDER" >/dev/null 2>&1 || true
docker buildx create --name "$BUILDER" --driver remote \
  --driver-opt "cacert=$BUILDKIT_CERTS_DIR/ca.pem,cert=$BUILDKIT_CERTS_DIR/cert.pem,key=$BUILDKIT_CERTS_DIR/key.pem,servername=buildkitd-windows.buildkit.svc.cluster.local" \
  tcp://10.152.184.40:1234

# --- Step 6: Build and push ---
FULL_IMAGE="$REGISTRY/$IMAGE_NAME"
OUTPUT_ARG="type=image,oci-mediatypes=false"
if $PUSH; then
  OUTPUT_ARG="type=image,push=true,oci-mediatypes=false"
fi

echo "--- Building Windows container image ---"
docker buildx build --builder "$BUILDER" \
  -f node/Dockerfile-windows.local \
  node/ \
  --platform windows/amd64 \
  --build-arg GIT_VERSION="$GIT_VERSION" \
  --build-arg WINDOWS_VERSION=ltsc2022 \
  -t "$FULL_IMAGE:$BASE_TAG" \
  -t "$FULL_IMAGE:$GIT_SHA" \
  --provenance=false \
  --output "$OUTPUT_ARG" \
  --cache-from "type=registry,ref=$FULL_IMAGE:buildcache-windows" \
  --cache-to "type=registry,ref=$FULL_IMAGE:buildcache-windows,mode=max"

echo ""
echo "=== Done ==="
echo "Image: $FULL_IMAGE:$BASE_TAG"
echo "Image: $FULL_IMAGE:$GIT_SHA"
if $PUSH; then
  echo "Pushed to Harbor."
else
  echo "(not pushed, use without --no-push to push)"
fi
