#!/usr/bin/env bash
# localtest/tftest-real/run.sh
#
# Local run test: build → reset mock → terraform apply → verify 1 open + 1 closed.
#
# Usage:
#   Terminal 1: go run ./localtest/mock-server/main.go
#   Terminal 2: bash localtest/tftest-real/run.sh
#
# Logs (no TF_LOG needed):
#   ~/.manifestit/provider-{ppid}.log         — provider lifecycle
#   ~/.manifestit/watcher-terraform-{ppid}.log — watcher subprocess (close event)

set -euo pipefail
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR="$REPO_ROOT/localtest/tftest-real"
LOGDIR="$HOME/.manifestit"

# ── 1. Build ──────────────────────────────────────────────────────────────────
echo ">>> Building provider binary..."
cd "$REPO_ROOT"
go build -o localtest/bin/terraform-provider-manifestit .
echo ">>> Binary ready: localtest/bin/terraform-provider-manifestit"

# ── 2. Clean old logs + reset mock server ─────────────────────────────────────
rm -f "$LOGDIR"/provider-*.log "$LOGDIR"/watcher-terraform-*.log 2>/dev/null || true
if curl -sf --max-time 2 http://localhost:8080/dump > /dev/null 2>&1; then
  curl -s -X POST http://localhost:8080/reset > /dev/null
  echo ">>> Mock server events cleared (localhost:8080)"
else
  echo ">>> WARNING: mock server not reachable on localhost:8080"
  echo ">>>   Start it first: go run ./localtest/mock-server/main.go"
fi

# ── 3. Terraform apply ────────────────────────────────────────────────────────
rm -f "$DIR/terraform.tfstate" "$DIR/terraform.tfstate.backup"
cd "$DIR"

echo ""
echo ">>> Running terraform apply..."
echo ">>> (null_resource sleeps 10s — proves closed fires after all resources)"
echo "================================================================="
echo ""

APPLY_START=$(date -u +%T)
TF_CLI_CONFIG_FILE="./.terraformrc" terraform apply -auto-approve
APPLY_END=$(date -u +%T)

echo ""
echo "================================================================="
echo ">>> apply start:  $APPLY_START"
echo ">>> apply finish: $APPLY_END"

# ── 4. Wait for watcher ───────────────────────────────────────────────────────
echo ">>> Waiting 8s for watcher subprocess to fire PATCH /closed..."
sleep 8
WAIT_END=$(date -u +%T)
echo ">>> wait done:    $WAIT_END"

# ── 5. Provider log ───────────────────────────────────────────────────────────
echo ""
echo ">>> PROVIDER LOG ($LOGDIR/provider-*.log):"
echo "-----------------------------------------------------------------"
PLOGS=$(ls "$LOGDIR"/provider-*.log 2>/dev/null)
if [[ -n "$PLOGS" ]]; then cat $PLOGS; else echo "(no provider log found)"; fi
echo "-----------------------------------------------------------------"

# ── 6. Watcher log ────────────────────────────────────────────────────────────
echo ""
echo ">>> WATCHER SUBPROCESS LOG ($LOGDIR/watcher-terraform-*.log):"
echo "-----------------------------------------------------------------"
WLOG=$(ls -t "$LOGDIR"/watcher-terraform-*.log 2>/dev/null | head -1)
if [[ -n "$WLOG" ]]; then cat "$WLOG"; else echo "(no watcher log found)"; fi
echo "-----------------------------------------------------------------"

# ── 7. Event summary + pass/fail ─────────────────────────────────────────────
echo ""
echo ">>> EVENTS (localhost:8080):"
curl -s http://localhost:8080/dump | python3 -c "
import json, sys
evs = json.load(sys.stdin)
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
        print('  PASS — 1 open + 1 closed, same run_id, closed after all resources done')
        print(f'  open={ot}  closed={ct}')
    else:
        print(f'  FAIL — run_id mismatch: open={oid[:8]} closed={cid[:8]}')
else:
    print(f'  FAIL — expected 1 open + 1 closed, got {len(opens)} open + {len(closes)} closed')
    print('  Check the provider and watcher logs above for errors')
"

