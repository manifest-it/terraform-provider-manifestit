#!/usr/bin/env bash
# localtest/tftest-real/test.sh
#
# End-to-end test against localhost:3000
# Verifies exactly 1 open + 1 closed event, closed fires AFTER all resources done.
#
# Usage:  bash localtest/tftest-real/test.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WATCHER_TMPDIR="$(python3 -c 'import tempfile; print(tempfile.gettempdir())')"

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[1;33m'; NC='\033[0m'
info()  { echo -e "${GREEN}[test]${NC} $*"; }
warn()  { echo -e "${YELLOW}[test]${NC} $*"; }
error() { echo -e "${RED}[test]${NC} $*"; }

# ── 1. Check localhost:3000 is reachable ──────────────────────────────────────
if ! curl -sf --max-time 3 http://localhost:8080/dump > /dev/null 2>&1; then
  warn "localhost:8080 mock server not reachable — start it first"
  warn "Run: go run ./localtest/mock-server/main.go &"
fi

# ── 2. Build binary ───────────────────────────────────────────────────────────
info "Building provider binary..."
cd "$REPO_ROOT"
go build -o localtest/bin/terraform-provider-manifestit .
info "Binary built: localtest/bin/terraform-provider-manifestit"

# ── 3. Clean old watcher logs ─────────────────────────────────────────────────
rm -f "$WATCHER_TMPDIR"/manifestit-watcher-*.log 2>/dev/null || true
info "Cleared old watcher logs from $WATCHER_TMPDIR"

# ── 4. Terraform apply ────────────────────────────────────────────────────────
cd "$REPO_ROOT/localtest/tftest-real"
rm -f terraform.tfstate terraform.tfstate.backup

# Reset mock server events so we get a clean slate
curl -s -X POST http://localhost:8080/reset > /dev/null 2>&1 && info "Mock server events cleared" || true

info "Running terraform apply..."
info "  → null_resource sleeps 10s (simulates slow AWS/GCP resources)"
info "  → manifestit_observer fires POST /open immediately"
info "  → watcher subprocess fires PATCH /closed after terraform exits"
echo ""

APPLY_START=$(date -u +%T)
TF_CLI_CONFIG_FILE="./.terraformrc" terraform apply -auto-approve
APPLY_END=$(date -u +%T)

echo ""
info "Apply start:  $APPLY_START"
info "Apply finish: $APPLY_END"

# ── 5. Wait for watcher ───────────────────────────────────────────────────────
info "Waiting 8s for watcher subprocess to detect terraform has exited..."
sleep 8
WAIT_END=$(date -u +%T)
info "Wait done:    $WAIT_END"

# ── 6. Show watcher log ───────────────────────────────────────────────────────
echo ""
info "=== WATCHER SUBPROCESS LOG ==="
WATCHER_LOGS=("$WATCHER_TMPDIR"/manifestit-watcher-*.log)
if [[ ! -f "${WATCHER_LOGS[0]:-}" ]]; then
  warn "No watcher log found — provider may have detected a no-op apply"
  warn "Check: did terraform show any resource changes?"
else
  LATEST_LOG=$(ls -t "$WATCHER_TMPDIR"/manifestit-watcher-*.log | head -1)
  info "Log: $LATEST_LOG"
  echo "---"
  cat "$LATEST_LOG"
  echo "---"
fi

# ── 7. Verify result ──────────────────────────────────────────────────────────
echo ""
info "=== RESULT ==="
if [[ -f "${WATCHER_LOGS[0]:-}" ]]; then
  LATEST_LOG=$(ls -t "$WATCHER_TMPDIR"/manifestit-watcher-*.log | head -1)
  if grep -q "PATCH /closed OK" "$LATEST_LOG" 2>/dev/null; then
    info "✅ PASS — open event + closed event both sent to http://localhost:8080"
    info "    open:   fired at apply start ($APPLY_START)"
    info "    closed: fired after ALL resources done (~$WAIT_END)"
    grep "run_id" "$LATEST_LOG" | head -1 | sed 's/^/    /'
  elif grep -q "PATCH /closed FAILED" "$LATEST_LOG" 2>/dev/null; then
    error "❌ PATCH /closed FAILED — server rejected it"
    grep "FAILED" "$LATEST_LOG"
    error "Check your server logs at localhost:8080"
    exit 1
  elif grep -q "done at" "$LATEST_LOG" 2>/dev/null; then
    info "✅ PASS — watcher completed (check server for events)"
  else
    warn "Watcher ran but result unclear — check log above"
  fi
fi

# ── 8. Dump mock server events ────────────────────────────────────────────────
echo ""
info "=== MOCK SERVER EVENTS (localhost:8080) ==="
curl -s http://localhost:8080/dump | python3 -c "
import json, sys
evs = json.load(sys.stdin)
if not evs:
    print('  (no events)')
else:
    print(f'  Total events: {len(evs)}')
    for e in evs:
        b = e['body'] if isinstance(e['body'], dict) else json.loads(e['body'])
        rid = b.get('run_id', e['path'].split('/')[-1])
        print(f\"  [{e['method']:5}] {e['received_at'][11:19]}  status={b.get('status','?'):8}  run_id={rid[:8]}\")
    opens  = [e for e in evs if (e['body'] if isinstance(e['body'],dict) else json.loads(e['body'])).get('status')=='open']
    closes = [e for e in evs if (e['body'] if isinstance(e['body'],dict) else json.loads(e['body'])).get('status')=='closed']
    print()
    print(f'  open events:   {len(opens)}  (want 1)')
    print(f'  closed events: {len(closes)} (want 1)')
    if opens and closes:
        oid = (opens[0]['body'] if isinstance(opens[0]['body'],dict) else json.loads(opens[0]['body']))['run_id']
        cid = closes[0]['path'].split('/')[-1]
        print(f'  run_id match:  {oid==cid}  ({oid[:8]})')
"

