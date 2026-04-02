#!/usr/bin/env bash
# localtest/ci-simulate.sh
#
# Simulates what a CI/CD runner does:
#
#   1. Sets CI environment variables (mimics GitHub Actions)
#   2. Runs terraform apply as a child process with those env vars
#   3. After the open event appears, sends SIGTERM to terraform —
#      exactly what a CI runner does when it tears down a job step
#   4. Verifies the provider's in-process signal handler fired the
#      "closed" event BEFORE the process was killed by the cgroup
#
# The key difference from a normal local run (run.sh):
#   - No waiting for the watcher subprocess — it would be killed by cgroup teardown
#   - The closed event MUST come from the in-process SIGTERM signal handler
#   - The closed event identity must contain ci_provider = "github-actions"
#
# Usage:
#   cd /Users/gauravpatil/mit-prod/repos/terraform-provider-manifestit/localtest
#   bash ci-simulate.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOCALTEST_DIR="$REPO_ROOT/localtest"
BIN_DIR="$LOCALTEST_DIR/bin"
TF_DIR="$LOCALTEST_DIR/terraform"
MOCK_PORT=8081
MOCK_PID_FILE="$LOCALTEST_DIR/.mock-ci.pid"
TF_PID_FILE="$LOCALTEST_DIR/.tf-ci.pid"
CI_TF_DIR="$LOCALTEST_DIR/terraform-ci"
TF_LOG_FILE="$LOCALTEST_DIR/tf-ci.log"
TERRAFORMRC="$TF_DIR/.terraformrc"

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${GREEN}[ci-sim]${NC} $*"; }
step()  { echo -e "${CYAN}[ci-sim]${NC} $*"; }
warn()  { echo -e "${YELLOW}[ci-sim]${NC} $*"; }
error() { echo -e "${RED}[ci-sim]${NC} $*"; }

cleanup() {
  if [[ -f "$TF_PID_FILE" ]]; then
    kill "$(cat "$TF_PID_FILE")" 2>/dev/null || true
    rm -f "$TF_PID_FILE"
  fi
  if [[ -f "$MOCK_PID_FILE" ]]; then
    kill "$(cat "$MOCK_PID_FILE")" 2>/dev/null || true
    rm -f "$MOCK_PID_FILE"
  fi
}
trap cleanup EXIT

# ── 1. Build ──────────────────────────────────────────────────────────────────
step "1/5  Building provider binary..."
cd "$REPO_ROOT"
go build -o "$BIN_DIR/terraform-provider-manifestit" .
info "Binary ready."

# ── 2. Start mock server ──────────────────────────────────────────────────────
step "2/5  Starting mock server on :$MOCK_PORT..."
lsof -ti ":$MOCK_PORT" | xargs kill -9 2>/dev/null || true
sleep 0.3
MOCK_ADDR=":$MOCK_PORT" go run "$LOCALTEST_DIR/mock-server/main.go" &
MOCK_PID=$!
echo "$MOCK_PID" > "$MOCK_PID_FILE"
for i in $(seq 1 20); do
  if curl -sf "http://localhost:$MOCK_PORT/dump" >/dev/null 2>&1; then
    info "Mock server ready (pid $MOCK_PID)"; break
  fi
  sleep 0.3
  [[ $i -eq 20 ]] && { error "Mock server did not start."; exit 1; }
done

# ── 3. Write CI terraform config pointing at the CI mock port ─────────────────
step "3/5  Writing CI terraform config..."
mkdir -p "$CI_TF_DIR"
cat > "$CI_TF_DIR/main.tf" <<EOF
terraform {
  required_providers {
    manifestit = { source = "registry.terraform.io/manifest-it/manifestit" }
  }
}
provider "manifestit" {
  api_key                   = "ci-test-key"
  api_url                   = "http://localhost:${MOCK_PORT}"
  validate                  = "false"
  org_id                    = 1
  org_key                   = "ci-org"
  provider_id               = 1
  provider_configuration_id = 1
  tracked_branch            = "main"
  tracked_repo              = "https://github.com/manifest-it/terraform-provider-manifestit.git"
}
resource "manifestit_observer" "this" {}
EOF
cp "$TERRAFORMRC" "$CI_TF_DIR/.terraformrc"

# ── 4. Simulate CI run: apply + SIGTERM ───────────────────────────────────────
step "4/5  Running terraform apply with GitHub Actions env vars, then sending SIGTERM..."

# Clear any leftover events
curl -sf -X POST "http://localhost:$MOCK_PORT/reset" || true
rm -f "$CI_TF_DIR/terraform.tfstate" "$CI_TF_DIR/terraform.tfstate.backup"
cd "$CI_TF_DIR"

# Run terraform in background with CI env vars explicitly set.
# This is what the GitHub Actions runner does — all CI env vars are in the env
# of the process it launches.
env \
  GITHUB_ACTIONS="true" \
  GITHUB_WORKFLOW="terraform-ci" \
  GITHUB_JOB="apply" \
  GITHUB_RUN_ID="99887766" \
  GITHUB_ACTOR="ci-bot" \
  GITHUB_EVENT_NAME="push" \
  GITHUB_SERVER_URL="https://github.com" \
  GITHUB_REPOSITORY="manifest-it/terraform-provider-manifestit" \
  TF_CLI_CONFIG_FILE=".terraformrc" \
  terraform apply -auto-approve >"$TF_LOG_FILE" 2>&1 &
TF_PID=$!
echo "$TF_PID" > "$TF_PID_FILE"
info "terraform started (pid $TF_PID)"

# Wait until the open event arrives
info "Waiting for open event..."
for i in $(seq 1 40); do
  EVENTS=$(curl -sf "http://localhost:$MOCK_PORT/dump" || echo "[]")
  if echo "$EVENTS" | grep -q '"open"'; then
    info "Open event received — sending SIGTERM to terraform (pid $TF_PID) to simulate CI teardown"
    break
  fi
  sleep 0.3
  [[ $i -eq 40 ]] && { error "Timed out waiting for open event"; cat "$TF_LOG_FILE"; exit 1; }
done

# Send SIGTERM — this is what the CI runner/cgroup teardown does
kill -TERM "$TF_PID" 2>/dev/null || true
rm -f "$TF_PID_FILE"

# ── 5. Verify ─────────────────────────────────────────────────────────────────
step "5/5  Verifying closed event was fired by signal handler (up to 15s)..."
CLOSED_SEEN=false
for i in $(seq 1 30); do
  EVENTS=$(curl -sf "http://localhost:$MOCK_PORT/dump" || echo "[]")
  if echo "$EVENTS" | grep -q '"closed"'; then
    CLOSED_SEEN=true; break
  fi
  sleep 0.5
done

echo ""
info "Events captured:"
echo "$EVENTS" | python3 -m json.tool 2>/dev/null || echo "$EVENTS"
echo ""

FAIL=0

# ── Check: open event ─────────────────────────────────────────────────────────
OPEN_COUNT=$(echo "$EVENTS" | python3 -c "
import json,sys
evs=json.load(sys.stdin)
print(sum(1 for e in evs if e.get('body',{}).get('status')=='open'))
" 2>/dev/null || echo 0)
if [[ "$OPEN_COUNT" -ge 1 ]]; then
  info "PASS: open event received"
else
  error "FAIL: no open event received"; FAIL=1
fi

# ── Check: closed event fired by signal handler ───────────────────────────────
if [[ "$CLOSED_SEEN" = true ]]; then
  info "PASS: closed event received after SIGTERM (signal handler fired)"
else
  error "FAIL: no closed event after SIGTERM — signal handler did not fire in time"; FAIL=1
fi

# ── Check: CI identity in closed event ───────────────────────────────────────
CI_PROVIDER=$(echo "$EVENTS" | python3 -c "
import json,sys
evs=json.load(sys.stdin)
for e in evs:
    b=e.get('body',{})
    if b.get('status')=='closed':
        print(b.get('identity',{}).get('ci_provider',''))
        break
" 2>/dev/null || true)

if [[ "$CI_PROVIDER" == "github-actions" ]]; then
  info "PASS: closed event identity.ci_provider = 'github-actions'"
else
  error "FAIL: ci_provider='$CI_PROVIDER' (expected 'github-actions') — identity was not collected during Configure()"
  FAIL=1
fi

# ── Check: run_id matches between open and closed ─────────────────────────────
OPEN_RUN_ID=$(echo "$EVENTS" | python3 -c "
import json,sys
evs=json.load(sys.stdin)
for e in evs:
    b=e.get('body',{})
    if b.get('status')=='open': print(b.get('run_id','')); break
" 2>/dev/null || true)

CLOSED_RUN_ID=$(echo "$EVENTS" | python3 -c "
import json,sys
evs=json.load(sys.stdin)
for e in evs:
    b=e.get('body',{})
    if b.get('status')=='closed': print(e.get('path','').split('/')[-1]); break
" 2>/dev/null || true)

if [[ -n "$OPEN_RUN_ID" && -n "$CLOSED_RUN_ID" && "$OPEN_RUN_ID" == "$CLOSED_RUN_ID" ]]; then
  info "PASS: run_id matches across open and closed events ($OPEN_RUN_ID)"
elif [[ -n "$OPEN_RUN_ID" && -n "$CLOSED_RUN_ID" ]]; then
  error "FAIL: run_id mismatch — open='$OPEN_RUN_ID' closed='$CLOSED_RUN_ID'"; FAIL=1
fi

echo ""
if [[ $FAIL -eq 0 ]]; then
  info "All CI simulation checks passed ✓"
else
  error "One or more checks failed ✗"; exit 1
fi

