# ManifestIT Provider — End-to-End Test Steps

This guide walks through testing the two-event lifecycle (`open` → `closed`) that
the ManifestIT provider fires on every `terraform apply` / `destroy`.

---

## What you are testing

| Event | When it fires |
|-------|--------------|
| `POST /open` | Immediately when `Configure()` runs at apply start |
| `PATCH /closed` | After **all** terraform resources finish — fired by a detached watcher subprocess that polls terraform's PID |

No `depends_on` is required in your terraform config.

---

## Prerequisites

- Go ≥ 1.21
- Terraform ≥ 1.0
- Two terminal windows

---

## Step 1 — Build the provider binary

```bash
cd terraform-provider-manifestit
go build -o localtest/bin/terraform-provider-manifestit .
```

---

## Step 2 — Start the mock server (Terminal 1)

```bash
go run ./localtest/mock-server/main.go
```

Events are printed in colour as they arrive:

| Colour | Status |
|--------|--------|
| 🟢 Green  | `open` |
| 🔴 Red    | `closed` |
| 🟡 Yellow | `heartbeat` |

---

## Step 3 — Run the test (Terminal 2)

Use the provided `run.sh` script which builds the binary, resets events, runs
`terraform apply`, waits for the watcher, and prints a pass/fail summary:

```bash
bash localtest/tftest-real/run.sh
```

Or manually:

```bash
# Reset mock server events
curl -s -X POST http://localhost:8080/reset

cd localtest/tftest-real
rm -f terraform.tfstate terraform.tfstate.backup

TF_CLI_CONFIG_FILE="./.terraformrc" terraform apply -auto-approve
```

### Expected terraform output

```
manifestit_observer.this: Creating...
manifestit_observer.this: Creation complete after 0s
null_resource.heartbeat_test_delay: Creating...
null_resource.heartbeat_test_delay: Still creating... [10s elapsed]
null_resource.heartbeat_test_delay: Creation complete after 10s
Apply complete! Resources: 3 added, 0 changed, 0 destroyed.
```

The `manifestit_observer` completes in 0s — the `closed` event does **not** fire
here. It fires only after `null_resource` (simulating slow AWS/GCP resources)
also finishes.

---

## Step 4 — Wait for the watcher subprocess

The watcher subprocess runs detached from the provider. It polls terraform's PID
every 2 seconds. After terraform exits, it fires `PATCH /closed`.

```bash
sleep 6
```

---

## Step 5 — Verify events

```bash
curl -s http://localhost:8080/dump
```

**Expected — exactly 2 events with the same `run_id`:**

```
[POST]  HH:MM:SS  status=open    run_id=XXXXXXXX   ← apply start
[PATCH] HH:MM:SS  status=closed  run_id=XXXXXXXX   ← after ALL resources done
```

The `closed` timestamp must be **≥10s after `open`** (after `null_resource` finishes).

---

## Step 6 — Read the provider log (no TF_LOG needed)

The provider writes a log file automatically on every run:

```bash
# macOS
cat /var/folders/*/*/T/manifestit-provider-*.log

# Linux / CI
cat /tmp/manifestit-provider-*.log
```

Every lifecycle step is visible:

```
[HH:MM:SS] detected operation=apply pid=XXXXX ppid=XXXXX
[HH:MM:SS] lifecycle start  operation=apply run_id=XXXXXXXX
[HH:MM:SS] identity/git collected  identity_type=local git_available=true
[HH:MM:SS] POST /open  run_id=XXXXXXXX base_url=http://localhost:8080
[HH:MM:SS] POST /open OK
[HH:MM:SS] heartbeat goroutine started  interval=30s
[HH:MM:SS] SIGTERM handler registered
[HH:MM:SS] watcher subprocess spawned  ppid=XXXXX
[HH:MM:SS] lifecycle setup complete — watcher will fire PATCH /closed when PPID exits
[HH:MM:SS] lock already held — skipping lifecycle (another instance owns this run)
```

The last line appears because terraform spawns the provider binary multiple times
per apply. The lock ensures only the **first** instance fires `POST /open`.

---

## Step 7 — Read the watcher subprocess log

```bash
# macOS
cat /var/folders/*/*/T/manifestit-watcher-*.log

# Linux / CI
cat /tmp/manifestit-watcher-*.log
```

```
manifestit-watcher: started, run_id=XXXXXXXX ppid=XXXXX base_url=http://localhost:8080
manifestit-watcher: polling ppid=XXXXX every 2s
manifestit-watcher: ppid=XXXXX exited, firing close event at YYYY-MM-DDTHH:MM:SSZ
manifestit: PATCH /closed OK (run_id=XXXXXXXX)
manifestit-watcher: done at YYYY-MM-DDTHH:MM:SSZ
```

If `PATCH /closed FAILED` appears here, the issue is with the API server
(wrong URL, auth, or network). The `base_url` in the log confirms what endpoint
was used.

---

## Step 8 — CI flow test (SIGTERM mid-apply)

This simulates a CI runner tearing down a job step while `terraform apply` is
still running:

```bash
curl -s -X POST http://localhost:8080/reset
cd localtest/tftest-real
rm -f terraform.tfstate terraform.tfstate.backup

# Start apply in background
TF_CLI_CONFIG_FILE="./.terraformrc" terraform apply -auto-approve &
TF_PID=$!

# After 3s (null_resource still running), send SIGTERM — simulates CI teardown
sleep 3
kill -TERM $TF_PID
wait $TF_PID

sleep 3
curl -s http://localhost:8080/dump
```

**Expected:** Same 2 events — `closed` fires within ~1s of SIGTERM (via the
SIGTERM handler, not the watcher) because the CI runner is killing the job.

---

## Summary — Pass criteria

| Check | Expected |
|-------|----------|
| `open` events received | **1** |
| `closed` events received | **1** |
| `run_id` on `open` == `run_id` on `closed` | **yes** |
| `closed` fires after all resources done | **yes** (≥10s after `open` in this test) |
| `depends_on` in terraform config required | **no** |
| SIGTERM triggers immediate `closed` (CI) | **yes** |
| Provider log file written automatically | **yes** — `$TMPDIR/manifestit-provider-{ppid}.log` |
| Watcher log file written automatically | **yes** — `$TMPDIR/manifestit-watcher-{ppid}.log` |

---

## Debugging production failures

If you get `open` but no `closed` in your real environment:

1. Check the watcher log — it shows exactly what happened:
   ```bash
   cat /tmp/manifestit-watcher-*.log   # Linux/CI
   ```

2. If `PATCH /closed FAILED` — check API URL, auth key, and network connectivity
   from the machine running terraform.

3. If no watcher log exists — check for `watcher spawn FAILED` in the provider
   log. This means `os.Executable()` returned a bad path (rare, usually in
   containerised environments where `/proc/self/exe` is restricted).

4. If the provider log shows `detected operation=unknown` — the provider could
   not read the parent process command line. This is the CI detection fallback;
   the SIGTERM handler still covers the CI close path.

