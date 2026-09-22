#!/bin/bash
# Run Calico's AppMana script suite in a Labcontainers-owned test session.
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
cd "$SCRIPT_DIR/lab"
exec go run . script-tests
