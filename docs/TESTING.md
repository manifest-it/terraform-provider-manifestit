# Testing

## Local `terraform apply`

Verifies the watcher subprocess fires `PATCH /closed` **after** all resources finish.

**Terminal 1 — start mock server:**
```bash
go run ./localtest/mock-server/main.go
```

**Terminal 2 — run test:**
```bash
bash localtest/tftest-real/run.sh
```

The test builds the binary, runs `terraform apply` (includes a 10s `null_resource` to prove
closed fires after all resources, not just the observer), waits for the watcher, and prints
a pass/fail verdict.

**Expected output:**
```
PASS — 1 open + 1 closed, same run_id, closed after all resources done
open=HH:MM:SS  closed=HH:MM:SS
```

The `closed` timestamp must be **after** `apply finish` — proving closed waits for
all resources, not just `manifestit_observer`.

---

## CI/CD (SIGTERM)

Simulates a CI runner killing a job mid-apply. Verifies the SIGTERM handler fires
`PATCH /closed` immediately.

**Terminal 1 — start mock server:**
```bash
go run ./localtest/mock-server/main.go
```

**Terminal 2 — run CI test:**
```bash
bash localtest/tftest-real/ci-test.sh
```

The script starts `terraform apply` in the background, sends SIGTERM after 3s
(while the 10s `null_resource` is still running), and verifies closed fires within ~1s.

**Expected output:**
```
PASS — SIGTERM fired PATCH /closed immediately
open=HH:MM:SS  closed=HH:MM:SS
```

---

## Production debugging

Logs are written automatically — no `TF_LOG` needed.

**macOS (dev machine):**
```bash
cat ~/.manifestit/provider-*.log           # provider lifecycle
cat ~/.manifestit/watcher-terraform-*.log  # watcher close event
```

**Linux / EKS container:**
```bash
cat /tmp/.manifestit/provider-*.log
cat /tmp/.manifestit/watcher-terraform-*.log
```

**What to look for:**

```
POST /open OK run_id=XXXX           ✅ open event fired
watcher spawned ppid=XXXX           ✅ watcher started
SIGTERM — firing PATCH /closed      ✅ CI teardown path
watcher: terraform exited, firing close  ✅ normal completion path
PATCH /closed OK run_id=XXXX        ✅ close confirmed
PATCH /closed FAILED ...            ❌ network/auth issue — check api_url and api_key
watcher spawn FAILED                ❌ binary not executable or /tmp full
```

**If you get `open` but no `closed`:**

1. Check `watcher-terraform-*.log` — `PATCH /closed FAILED` means API URL or auth is wrong
2. Check `provider-*.log` for `watcher spawn FAILED` — means `os.Executable()` failed
   (can happen in heavily sandboxed environments)
3. Check `operation=unknown` in provider log — parent cmdline unreadable;
   set `TF_REATTACH_PROVIDERS` or check if `ps` is available in the container

---

## Running unit tests

```bash
go test ./... -count=1 -race
```
</content>
</invoke>
