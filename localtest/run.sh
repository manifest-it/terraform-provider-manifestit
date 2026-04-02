#!/usr/bin/env bash
# localtest/run.sh
# One-shot script that:
#   1. Builds the provider binary
#   2. Starts the mock server in the background
#   3. Runs terraform init + apply + destroy
#   4. Prints all events captured by the mock server
#   5. Verifies an "open" and a "closed" event were both received
#
# Usage:
#   cd localtest && bash run.sh
#
# Prerequisites:
#   - terraform in PATH
#   - Go toolchain in PATH

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOCALTEST_DIR="$REPO_ROOT/localtest"
BIN_DIR="$LOCALTEST_DIR/bin"
TF_DIR="$LOCALTEST_DIR/terraform"
MOCK_PORT=8080
MOCK_PID_FILE="$LOCALTEST_DIR/.mock-server.pid"
TERRAFORMRC="$TF_DIR/.terraformrc"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[run.sh]${NC} $*"; }
warn()  { echo -e "${YELLOW}[run.sh]${NC} $*"; }
error() { echo -e "${RED}[run.sh]${NC} $*"; }

cleanup() {
  if [[ -f "$MOCK_PID_FILE" ]]; then
    MPID=$(cat "$MOCK_PID_FILE")
    info "Stopping mock server (pid $MPID)..."
    kill "$MPID" 2>/dev/null || true
    rm -f "$MOCK_PID_FILE"
  fi
}
trap cleanup EXIT

# ── 1. Build provider binary ──────────────────────────────────────────────────
info "Building provider binary..."
mkdir -p "$BIN_DIR"
cd "$REPO_ROOT"
go build -o "$BIN_DIR/terraform-provider-manifestit" .
info "Binary: $BIN_DIR/terraform-provider-manifestit"

# ── 2. Start mock server ──────────────────────────────────────────────────────
info "Starting mock server on :$MOCK_PORT..."
MOCK_ADDR=":$MOCK_PORT" go run "$LOCALTEST_DIR/mock-server/main.go" &
MOCK_PID=$!
echo "$MOCK_PID" > "$MOCK_PID_FILE"

# Wait for mock to be ready
for i in $(seq 1 20); do
  if curl -sf "http://localhost:$MOCK_PORT/dump" >/dev/null 2>&1; then
    info "Mock server is ready."
    break
  fi
  sleep 0.3
  if [[ $i -eq 20 ]]; then
    error "Mock server did not start in time."
    exit 1
  fi
done

# ── 3. Terraform apply ────────────────────────────────────────────────────────
# NOTE: terraform init is intentionally skipped.
# dev_overrides in .terraformrc bypass the registry entirely — running
# terraform init would fail on the version constraint. Go straight to apply.
info "Running terraform apply (dev_overrides active — init not required)..."
cd "$TF_DIR"
# Remove any previous state so each run is clean.
rm -f terraform.tfstate terraform.tfstate.backup terraform.tfstate.d
rm -rf .terraform .terraform.lock.hcl

# ── 4. Terraform apply ────────────────────────────────────────────────────────
info "Running terraform apply..."
TF_CLI_CONFIG_FILE="$TERRAFORMRC" terraform apply -auto-approve

# Give the watcher subprocess up to 10 s to detect terraform has exited
# and fire the closed event before we query the mock server.
info "Waiting for watcher to fire closed event (up to 10 s)..."
CLOSED_SEEN=false
for i in $(seq 1 20); do
  EVENTS=$(curl -sf "http://localhost:$MOCK_PORT/dump")
  if echo "$EVENTS" | grep -q '"closed"'; then
    CLOSED_SEEN=true
    break
  fi
  sleep 0.5
done

# ── 5. Print captured events ──────────────────────────────────────────────────
info "Events captured by mock server:"
curl -sf "http://localhost:$MOCK_PORT/dump" | python3 -m json.tool || \
  curl -sf "http://localhost:$MOCK_PORT/dump"

# ── 6. Verify both events arrived ─────────────────────────────────────────────
EVENTS=$(curl -sf "http://localhost:$MOCK_PORT/dump")

OPEN_COUNT=$(echo "$EVENTS" | grep -c '"open"' || true)
CLOSED_COUNT=$(echo "$EVENTS" | grep -c '"closed"' || true)

echo ""
info "Results:"
echo "  open events  : $OPEN_COUNT (expected ≥1)"
echo "  closed events: $CLOSED_COUNT (expected ≥1)"

FAIL=0

if [[ "$OPEN_COUNT" -lt 1 ]]; then
  error "FAIL: no 'open' event received"
  FAIL=1
else
  info "PASS: open event received"
fi

if [[ "$CLOSED_SEEN" = false ]] || [[ "$CLOSED_COUNT" -lt 1 ]]; then
  error "FAIL: no 'closed' event received (watcher may have timed out)"
  FAIL=1
else
  info "PASS: closed event received"
fi

# Verify both events share the same run_id
OPEN_RUN_ID=$(echo "$EVENTS" | python3 -c "
import json,sys
evs = json.load(sys.stdin)
for e in evs:
    b = e.get('body', {})
    if isinstance(b, str): b = json.loads(b)
    if b.get('status') == 'open':
        print(b.get('run_id',''))
        break
" 2>/dev/null || true)

CLOSED_RUN_ID=$(echo "$EVENTS" | python3 -c "
import json,sys
evs = json.load(sys.stdin)
for e in evs:
    # closed event is a PATCH to /api/v1/events/{run_id}
    path = e.get('path','')
    b = e.get('body', {})
    if isinstance(b, str): b = json.loads(b)
    if b.get('status') == 'closed':
        # run_id is the last segment of the path
        print(path.split('/')[-1])
        break
" 2>/dev/null || true)

if [[ -n "$OPEN_RUN_ID" && -n "$CLOSED_RUN_ID" ]]; then
  if [[ "$OPEN_RUN_ID" == "$CLOSED_RUN_ID" ]]; then
    info "PASS: run_id matches across open and closed events ($OPEN_RUN_ID)"
  else
    error "FAIL: run_id mismatch — open='$OPEN_RUN_ID' closed='$CLOSED_RUN_ID'"
    FAIL=1
  fi
else
  warn "Could not extract run_id for comparison (python3 required for this check)"
fi

echo ""
if [[ $FAIL -eq 0 ]]; then
  info "All checks passed ✓"
else
  error "One or more checks failed ✗"
  exit 1
fi

