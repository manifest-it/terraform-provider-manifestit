#!/usr/bin/env bash
# localtest/tftest-real/ci-test.sh
#
# CI/SIGTERM path test: simulates a CI runner killing terraform mid-apply.
# Verifies the SIGTERM handler fires PATCH /closed immediately.
#
# Usage:
#   Terminal 1: go run ./localtest/mock-server/main.go
#   Terminal 2: bash localtest/tftest-real/ci-test.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR="$REPO_ROOT/localtest/tftest-real"
LOGDIR="$HOME/.manifestit"

# ── 1. Build ─────────────────────────────��────────────────────────────────────
echo ">>> Building provider binary..."
cd "$REPO_ROOT"
go build -o localtest/bin/terraform-provider-manifestit .
echo ">>> Binary ready"

# ── 2. Reset mock server ──────────────────────────────────────────────────────
rm -f "$LOGDIR"/provider-*.log "$LOGDIR"/watcher-terraform-*.log 2>/dev/null || true
if curl -sf --max-time 2 http://localhost:8080/dump > /dev/null 2>&1; then
  curl -s -X POST http://localhost:8080/reset > /dev/null
  echo ">>> Mock server events cleared (localhost:8080)"
else
  echo ">>> WARNING: mock server not reachable — start it first"
  echo ">>>   go run ./localtest/mock-server/main.go"
  exit 1
fi

# ── 3. Start terraform apply in background ───────────────────────────────────
rm -f "$DIR/terraform.tfstate" "$DIR/terraform.tfstate.backup"
cd "$DIR"

echo ""
echo ">>> Starting terraform apply in background..."
echo ">>> (will send SIGTERM after 3s — before the 10s slow resource finishes)"
echo "================================================================="
echo ""

TF_CLI_CONFIG_FILE="./.terraformrc" terraform apply -auto-approve &
TF_PID=$!
echo ">>> terraform pid=$TF_PID"

# ── 4. Send SIGTERM after 3s ──────────────────────────────────────────────────
sleep 3
echo ""
echo ">>> Sending SIGTERM to terraform (pid=$TF_PID) — simulating CI teardown..."
kill -TERM "$TF_PID" 2>/dev/null || true
wait "$TF_PID" 2>/dev/null || true
echo ">>> terraform exited"

# ── 5. Wait briefly for SIGTERM handler to fire PATCH /closed ────────────────
echo ">>> Waiting 3s for SIGTERM handler to fire PATCH /closed..."
sleep 3

# ── 6. Provider log ───────────────────────────────────────────────────────────
echo ""
echo ">>> PROVIDER LOG ($LOGDIR/provider-*.log):"
echo "-----------------------------------------------------------------"
PLOG=$(ls -t "$LOGDIR"/provider-*.log 2>/dev/null | head -1)
if [[ -n "$PLOG" ]]; then cat "$PLOG"; else echo "(no provider log found)"; fi
echo "-----------------------------------------------------------------"

# ── 7. Event summary + pass/fail ─────────────────────────────────────────────
echo ""
echo ">>> EVENTS (localhost:8080):"
curl -s http://localhost:8080/dump | python3 -c "
import json, sys
evs = json.load(sys.stdin) or []
print(f'  Total: {len(evs)} event(s)')
for e in evs:
    b = e['body'] if isinstance(e['body'], dict) else json.loads(e['body'])
    rid = b.get('run_id', e['path'].split('/')[-1])
    print(f\"  {e['method']:5} {e['received_at'][11:19]}  status={b.get('status','?'):9}  run_id={rid[:8]}\")

opens  = [e for e in evs if (e['body'] if isinstance(e['body'],dict) else json.loads(e['body'])).get('status')=='open']
closes = [e for e in evs if (e['body'] if isinstance(e['body'],dict) else json.loads(e['body'])).get('status')=='closed']
print()
print(f'  open:   {len(opens)}  (want 1)')
print(f'  closed: {len(closes)} (want 1)')
if len(opens)==1 and len(closes)==1:
    oid = (opens[0]['body'] if isinstance(opens[0]['body'],dict) else json.loads(opens[0]['body']))['run_id']
    cid = closes[0]['path'].split('/')[-1]
    ot  = opens[0]['received_at'][11:19]
    ct  = closes[0]['received_at'][11:19]
    if oid == cid:
        print(f'  run_id: {oid[:8]} matches on both events')
        print()
        print('  PASS — SIGTERM fired PATCH /closed immediately')
        print(f'  open={ot}  closed={ct}')
    else:
        print(f'  FAIL — run_id mismatch: open={oid[:8]} closed={cid[:8]}')
elif len(opens)==1 and len(closes)==0:
    print('  FAIL — open received but no closed — SIGTERM handler did not fire')
    print('  Check provider log for SIGTERM entry')
else:
    print(f'  FAIL — expected 1 open + 1 closed, got {len(opens)} open + {len(closes)} closed')
"

