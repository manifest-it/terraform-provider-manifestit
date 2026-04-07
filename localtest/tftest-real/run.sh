#!/usr/bin/env bash
# localtest/tftest-real/run.sh
#
# Runs terraform apply with provider-level logging visible in the terminal.
# Provider logs (tflog.Debug/Info/Warn) print to stderr via TF_LOG=provider.
# Mock server events print to the mock-server process stdout.
#
# Usage:
#   # terminal 1 — start mock server (see events arrive in colour):
#   go run ./localtest/mock-server/main.go
#
#   # terminal 2 — run this script:
#   bash localtest/tftest-real/run.sh
#
# What you will see:
#   [provider] lines   = ManifestIT provider internal logs (open/close/watcher/heartbeat)
#   [terraform] lines  = terraform resource progress
#   mock server stdout = colour-coded open/closed events as they arrive

set -euo pipefail
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR="$REPO_ROOT/localtest/tftest-real"

# Build latest binary
echo ">>> Building provider binary..."
cd "$REPO_ROOT"
go build -o localtest/bin/terraform-provider-manifestit .
echo ">>> Binary ready"

# Clean state so every run creates resources fresh
rm -f "$DIR/terraform.tfstate" "$DIR/terraform.tfstate.backup"

echo ""
echo ">>> Running terraform apply (provider logs visible below)"
echo ">>> Watch the mock-server terminal for colour-coded open/closed events"
echo "================================================================="
echo ""

cd "$DIR"

# TF_LOG=provider  → shows all provider plugin log lines (tflog.Debug/Info/Warn/Error)
# TF_LOG_PATH=/dev/stderr → keep logs in the same terminal stream
export TF_LOG=provider
export TF_LOG_PATH=/dev/stderr

TF_CLI_CONFIG_FILE="./.terraformrc" terraform apply -auto-approve

echo ""
echo "================================================================="
echo ">>> Apply done. Waiting 8s for watcher subprocess to fire PATCH /closed..."
sleep 8

# Show watcher log
WATCHER_TMPDIR="$(go env GOTMPDIR 2>/dev/null || python3 -c 'import tempfile; print(tempfile.gettempdir())')"
echo ""
echo ">>> Watcher subprocess log:"
echo "-----------------------------------------------------------------"
cat "$WATCHER_TMPDIR"/manifestit-watcher-*.log 2>/dev/null || \
  find /var/folders -name "manifestit-watcher-*.log" -newer "$REPO_ROOT/localtest/bin/terraform-provider-manifestit" 2>/dev/null \
    | sort -t- -k4 -n | tail -1 | xargs cat 2>/dev/null || \
  echo "(no watcher log found)"
echo "-----------------------------------------------------------------"

echo ""
echo ">>> Final events from mock server:"
curl -s http://localhost:8080/dump | python3 -c "
import json, sys
evs = json.load(sys.stdin)
print(f'Total: {len(evs)} event(s)')
for e in evs:
    b = e['body'] if isinstance(e['body'], dict) else json.loads(e['body'])
    rid = b.get('run_id', e['path'].split('/')[-1])
    print(f\"  {e['method']:5} {e['received_at'][11:19]}  status={b.get('status','?'):9}  run_id={rid[:8]}\")
opens  = [e for e in evs if (e['body'] if isinstance(e['body'],dict) else json.loads(e['body'])).get('status')=='open']
closes = [e for e in evs if (e['body'] if isinstance(e['body'],dict) else json.loads(e['body'])).get('status')=='closed']
print()
if len(opens)==1 and len(closes)==1:
    oid = (opens[0]['body'] if isinstance(opens[0]['body'],dict) else json.loads(opens[0]['body']))['run_id']
    cid = closes[0]['path'].split('/')[-1]
    if oid == cid:
        ot = opens[0]['received_at'][11:19]
        ct = closes[0]['received_at'][11:19]
        print(f'PASS: 1 open ({ot}) + 1 closed ({ct}), run_id={oid[:8]} matches')
    else:
        print('FAIL: run_id mismatch between open and closed')
else:
    print(f'FAIL: got {len(opens)} open, {len(closes)} closed (expected 1 each)')
"

