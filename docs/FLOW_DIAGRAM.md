# ManifestIT Provider — Complete Flow Diagram

## Two paths, one guarantee

Two independent paths exist to fire the `"closed"` event.  
`closeOnce` (`sync.Once`) ensures **exactly one** of them fires.

```
┌─────────────────────────────────────────────────────────────────────┐
│  Scenario              │ Signal to plugin? │ Which path fires?      │
├─────────────────────────────────────────────────────────────────────┤
│  Local apply finishes  │ None — gRPC close │ Watcher (polls PPID)   │
│  Local plugin stalls   │ SIGKILL (uncatch) │ Watcher (polls PPID)   │
│  ctrl+c during apply   │ SIGINT — eaten by │ Watcher (polls PPID)   │
│                        │ go-plugin server  │                        │
│  CI runner tears down  │ SIGTERM (direct,  │ Signal handler         │
│  job step              │ same process grp) │                        │
└─────────────────────────────────────────────────────────────────────┘
```

---

## High-Level Process Diagram

```
┌──────────────────────────────────────────────────────────────────────────┐
│                          terraform apply / destroy                        │
│                                                                           │
│  ┌──────────────┐  gRPC conn   ┌──────────────────────────────────────┐  │
│  │   terraform  │◄────────────►│       provider plugin process        │  │
│  │  (parent)    │              │ (terraform-provider-manifestit)       │  │
│  └──────┬───────┘              └────────────────┬─────────────────────┘  │
│         │                                       │                         │
│         │  same process group (no Setpgid set) ◄┘                         │
│         │                                                                  │
│         │                         ┌─────────────────────────────────┐     │
│         │                         │    watcher subprocess           │     │
│         │                         │  (same binary, Setsid=true,     │     │
│         │                         │   MIT_WATCHER_MODE=1)           │     │
│         │                         └─────────────────────────────────┘     │
└──────────────────────────────────────────────────────────────────────────┘
```

---

## Phase 1 — Configure() (fires on every `terraform apply` / `destroy`)

```
terraform apply
      │
      ▼
go-plugin starts provider plugin binary
(forwards all env vars including CI vars: GITHUB_ACTIONS, GITHUB_RUN_ID, etc.)
      │
      ▼
┌──────────────────────────────────────────────────────────┐
│  Configure() — provider plugin process                   │
│                                                          │
│  1. detectTerraformOperation()                           │
│     reads parent cmdline via ps -o args= -p PPID         │
│     → "apply" or "destroy"                               │
│     if NOT apply/destroy → skip everything, return       │
│                                                          │
│  2. acquireRunLock()                                     │
│     O_CREATE|O_EXCL on                                   │
│     $TMPDIR/manifestit-observer-{ppid}.lock              │
│     writes "ppid:uuid" atomically                        │
│     if lock held by live owner  → already posted, skip  │
│     if lock held by dead owner  → stale, reclaim it     │
│     → returns (runID, lockPath)                          │
│                                                          │
│  3. c.Collect() — identity + git (8s timeout)           │
│     CI env vars available here (go-plugin forwarded      │
│     os.Environ() to plugin subprocess)                   │
│     reads git HEAD, branch, remote, dirty state          │
│     → result.Identity, result.Git                        │
│                                                          │
│  4. POST /api/v1/events          ← EVENT 1: OPEN        │
│     { run_id, status:"open",                             │
│       action, collected_at, org_id }                     │
│     if API fails → remove lock, AddWarning, return       │
│                                                          │
│  5. writeWatcherState()                                  │
│     $TMPDIR/manifestit-watcher-{ppid}.json (mode 0600)  │
│     { run_id, ppid, action, api_key, base_url,           │
│       identity, git, tracked_branch, lock_path … }       │
│     if write fails → remove lock, AddWarning, return     │
│                                                          │
│  6. registerSignalHandler(state)    ← CI PATH armed     │
│     goroutine blocks on SIGTERM/SIGINT                   │
│     go-plugin does NOT register SIGTERM so our handler   │
│     is the first to receive it                           │
│                                                          │
│  7. spawnWatcher(statePath)         ← LOCAL PATH armed  │
│     re-executes same binary:                             │
│       MIT_WATCHER_MODE=1                                 │
│       MIT_WATCHER_STATE=/tmp/manifestit-watcher-{ppid}  │
│       Setsid=true (own session, detached from terminal)  │
│     if spawn fails → spawnInProcessWatcher(state)        │
│       fallback goroutine inside provider process         │
│                                                          │
│  → Configure() returns, terraform continues applying     │
└──────────────────────────────────────────────────────────┘
```

---

## Phase 2A — LOCAL path (normal `terraform apply` finishes)

```
terraform apply completes
      │
      ▼
terraform calls client.Kill() on provider plugin
      │
      ├─ client.Close() → drops gRPC connection
      │  → plugin's grpc.Server.Serve() returns
      │  → defer close(DoneCh) fires
      │  → plugin process exits cleanly
      │  (NO SIGTERM sent — signal handler goroutine never wakes)
      │
      └─ if plugin stalls >2s:
         cmd.Process.Kill() → SIGKILL (uncatchable)
         signal handler cannot catch SIGKILL either

      ▼
┌──────────────────────────────────────────────────────┐
│  Watcher subprocess (detached, own session)           │
│                                                       │
│  reads $TMPDIR/manifestit-watcher-{ppid}.json        │
│                                                       │
│  pollUntilDead():                                     │
│    every 2s: kill -0 {ppid}                          │
│      alive → sleep 2s                                │
│      dead  → break (terraform exited)                │
│    (4h safety cap)                                    │
│                                                       │
│  closeOnce.Do(fireCloseEvent)                         │
│    identity = state.Identity (pre-collected)          │
│    git      = state.Git      (pre-collected)          │
│    PATCH /api/v1/events/{run_id} ← EVENT 2: CLOSED   │
│    { status:"closed", identity, git,                  │
│      action, collected_at, org_id }                   │
│                                                       │
│  cleanup:                                             │
│    os.Remove(manifestit-watcher-{ppid}.json)         │
│    os.Remove(manifestit-observer-{ppid}.lock)        │
│  os.Exit(0)                                           │
└──────────────────────────────────────────────────────┘
```

---

## Phase 2B — CI/CD path (GitHub Actions, GitLab CI, etc.)

```
terraform apply completes
      │
      ▼
CI runner tears down the job step
      │
      ▼
CI runner sends SIGTERM to terraform's PROCESS GROUP
(kill -TERM -{pgid})
      │
      ├─► terraform receives SIGTERM directly
      │
      └─► provider plugin receives SIGTERM directly
          (same process group, no Setpgid set by go-plugin)
                │
                │  go-plugin only catches SIGINT (and ignores it)
                │  SIGTERM passes straight to our handler
                ▼
      ┌──────────────────────────────────────────────────┐
      │  Signal handler goroutine                         │
      │  (registered in Configure(), step 6)              │
      │                                                   │
      │  catches SIGTERM                                  │
      │                                                   │
      │  closeOnce.Do(fireCloseEvent)  ← exactly once    │
      │    identity = state.Identity (pre-collected)      │
      │    git      = state.Git      (pre-collected)      │
      │    PATCH /api/v1/events/{run_id} ← CLOSED        │
      │    25s timeout                                    │
      │                                                   │
      │  cleanup: remove .json and .lock files            │
      │                                                   │
      │  re-raise SIGTERM → plugin exits                 │
      │                                                   │
      │  Watcher subprocess: killed by cgroup teardown   │
      │  (closeOnce already fired above — no-op)         │
      └──────────────────────────────────────────────────┘
```

---

## Phase 2C — Fallback (watcher subprocess spawn fails)

```
spawnWatcher() returns error
(read-only filesystem, binary not executable, out of PIDs, etc.)
      │
      ▼
spawnInProcessWatcher(state) called
      │
      ▼
Goroutine starts inside the provider process itself:
      │
      ├─ polls kill -0 {ppid} every 2s (same logic as subprocess)
      │
      └─ when PPID gone:
         closeOnce.Do(fireCloseEvent)
         cleanup files

This guarantees the closed event fires even when the
subprocess cannot be created.
```

---

## Phase 2D — closeOnce race guard

```
Example: user sends SIGTERM manually during a local run
(signal handler fires) AND watcher polls (watcher fires simultaneously)

  Signal handler          Watcher subprocess
       │                        │
       ▼                        ▼
  closeOnce.Do()          closeOnce.Do()
  ┌─ WINS (first) ─┐      ┌─ BLOCKED ─┐
  │ fires PATCH    │      │  no-op     │
  └────────────────┘      └────────────┘

Exactly one PATCH /closed is ever sent per run.
```

---

## Timing Diagram

```
Time ──────────────────────────────────────────────────────────────────►

terraform apply
├── [0ms]     Configure() called
│             ├── detectTerraformOperation()    ~1ms
│             ├── acquireRunLock()              ~1ms   O_EXCL atomic
│             ├── c.Collect() identity+git      ~200-500ms (8s cap)
│             ├── POST /events (open)           ~50-200ms network
│             ├── writeWatcherState()           ~1ms
│             ├── registerSignalHandler()       ~0ms   goroutine start
│             └── spawnWatcher()               ~5ms   fork+exec
│
├── [~500ms]  Configure() returns → terraform applies resources
│
└── [T_end]   terraform finishes

   LOCAL                                 CI/CD
   ─────────────────────────────         ────────────────────────────
   terraform: client.Close()             CI runner: kill -TERM -{pgid}
   → gRPC drops                          → terraform gets SIGTERM
   → plugin exits (no signal)            → plugin gets SIGTERM directly
   → watcher: kill -0 detects            → signal handler fires inline
   → PATCH /closed  (~0-2s lag)          → PATCH /closed  (~0ms lag)
   → cleanup files                       → re-raise → exit → cgroup done
```

---

## Filesystem State

```
$TMPDIR/
├── manifestit-observer-{ppid}.lock      idempotency guard
│     content: "ppid:run_uuid"
│     created: acquireRunLock() with O_CREATE|O_EXCL
│     deleted: after closed event fires (watcher or signal handler)
│
└── manifestit-watcher-{ppid}.json       handoff between Configure() and close paths
      content: WatcherState JSON (mode 0600)
      fields:  run_id, ppid, action, api_key, base_url,
               org_id, org_key, provider_id,
               provider_configuration_id,
               tracked_branch, tracked_repo,
               identity { type, os_user, hostname,
                          ci_provider, ci_run_id, ci_actor … }
               git { branch, commit, dirty,
                     drift_detected, drift_reasons … }
      created: writeWatcherState() in Configure()
      deleted: after closed event fires
```

---

## API Events

```
                  ┌────────────────────────────────────┐
                  │        ManifestIT API               │
                  │                                     │
                  │  POST /api/v1/events                │
                  │  ← open event (minimal, fast)       │
                  │    run_id   : "uuid-v4"             │
                  │    status   : "open"                │
                  │    action   : "apply" | "destroy"   │
                  │    collected_at : RFC3339            │
                  │    org_id   : "42"                  │
                  │                                     │
                  │  PATCH /api/v1/events/{run_id}      │
                  │  ← closed event (full context)      │
                  │    status   : "closed"              │
                  │    action   : "apply" | "destroy"   │
                  │    collected_at : RFC3339            │
                  │    org_id   : "42"                  │
                  │    identity : {                     │
                  │      type        : "local" | "github-actions" | … │
                  │      os_user     : "gauravpatil"    │
                  │      hostname    : "machine.local"  │
                  │      os          : "darwin"         │
                  │      arch        : "arm64"          │
                  │      ci_provider : "github-actions" │
                  │      ci_run_id   : "99887766"       │
                  │      ci_actor    : "ci-bot"         │
                  │      ci_run_url  : "https://…"      │
                  │    }                                │
                  │    git : {                          │
                  │      branch          : "main"       │
                  │      commit          : "abc123"     │
                  │      dirty           : true         │
                  │      drift_detected  : true         │
                  │      drift_reasons   : ["uncommitted_changes"] │
                  │      repo_mismatch   : false        │
                  │    }                                │
                  └────────────────────────────────────┘
```

---

## Edge Case Map

```
Edge Case                    │ Handling
─────────────────────────────┼──────────────────────────────────────────────
Normal local exit            │ gRPC close → plugin exits → watcher polls PPID
                             │ → PATCH /closed. No signal involved.
─────────────────────────────┼──────────────────────────────────────────────
Local plugin stalls >2s      │ go-plugin sends SIGKILL (uncatchable)
                             │ Watcher polls PPID → PATCH /closed
─────────────────────────────┼──────────────────────────────────────────────
ctrl+c during apply          │ SIGINT → go-plugin catches and ignores it
                             │ Terraform exits → watcher polls → PATCH /closed
─────────────────────────────┼──────────────────────────────────────────────
CI runner tears down step    │ SIGTERM to process group → plugin gets it directly
                             │ go-plugin does NOT catch SIGTERM
                             │ Our signal handler fires → PATCH /closed
─────────────────────────────┼──────────────────────────────────────────────
Watcher subprocess fails     │ spawnInProcessWatcher() goroutine takes over
to spawn                     │ Same poll logic, same closeOnce guard
                             │ Closed event still guaranteed
─────────────────────────────┼──────────────────────────────────────────────
API down (POST open fails)   │ Lock removed → terraform continues (Warning)
                             │ No watcher spawned → clean retry next run
─────────────────────────────┼──────────────────────────────────────────────
State file write fails       │ Lock removed → clean retry next run (Warning)
─────────────────────────────┼──────────────────────────────────────────────
API down (PATCH closed)      │ MaxRetries=3 with backoff attempted
                             │ After retries exhausted, process exits normally
─────────────────────────────┼──────────────────────────────────────────────
Configure() called twice     │ acquireRunLock() O_EXCL → second call gets
(same terraform run)         │ alreadyPosted=true → silently skipped
─────────────────────────────┼──────────────────────────────────────────────
Stale lock from crashed run  │ acquireRunLock() checks kill -0 on lock owner
                             │ Owner dead → lock reclaimed → fresh run
─────────────────────────────┼──────────────────────────────────────────────
Not apply/destroy (plan)     │ detectTerraformOperation() → skipped entirely
─────────────────────────────┼──────────────────────────────────────────────
Git not available            │ result.Git.Available=false → stored as-is
                             │ PATCH /closed still fires
─────────────────────────────┼──────────────────────────────────────────────
Both paths fire at once      │ sync.Once (closeOnce) → first wins, second no-op
                             │ Exactly one PATCH /closed per run
─────────────────────────────┼──────────────────────────────────────────────
terraform runs >4h           │ Watcher exits after 4h safety cap, fires anyway
─────────────────────────────┼──────────────────────────────────────────────
CI env vars in closed event  │ go-plugin forwards os.Environ() to plugin
                             │ Identity collected in Configure() where CI vars
                             │ are present, stored in WatcherState, reused
                             │ by both signal handler and watcher subprocess
```

