# ManifestIT Provider — Architecture Overview

## What this provider does

On every `terraform apply` or `terraform destroy`, the provider fires two events:

1. **`open`** — immediately when `Configure()` is called, before any resources are touched
2. **`closed`** — when the terraform process finishes, with full git context and identity

Both events carry the same `run_id` (UUID v4) so the server can correlate them.

---

## Component reference

### `internal/provider/init.go`

| Function | Purpose |
|---|---|
| `Configure()` | Entry point — wires everything together |
| `postObserverData()` | Orchestrates the full open/close lifecycle |
| `detectTerraformOperation()` | Reads parent process cmdline to detect `apply` / `destroy` |
| `acquireRunLock()` | Atomic `O_EXCL` file lock — exactly one event per terraform run |
| `generateRunID()` | UUID v4 via `github.com/google/uuid` |

### `internal/provider/watcher.go`

| Function | Purpose |
|---|---|
| `WatcherState` | Struct serialised to `$TMPDIR/manifestit-watcher-{ppid}.json` |
| `writeWatcherState()` / `readWatcherState()` | Disk I/O handoff between `Configure()` and close paths |
| `registerSignalHandler()` | **CI path** — catches SIGTERM sent directly to the process group |
| `spawnWatcher()` | **Local path** — detached subprocess (Setsid) polls PPID |
| `spawnInProcessWatcher()` | **Fallback** — in-process goroutine when subprocess spawn fails |
| `WatcherMain()` | Entry point when binary runs as `MIT_WATCHER_MODE=1` |
| `pollUntilDead()` | Polls `kill -0 {ppid}` every 2 s, fires close event on exit |
| `fireCloseEvent()` | Sends `PATCH /api/v1/events/{run_id}` with pre-collected identity + git |
| `closeOnce` | `sync.Once` — guarantees exactly one `PATCH /closed` per run |

### `pkg/sdk/providers/observer/observer.go`

| Method | Event | HTTP call |
|---|---|---|
| `Post(ctx, ObserverPayload)` | `open` | `POST /api/v1/events` |
| `Patch(ctx, runID, ClosePayload)` | `closed` | `PATCH /api/v1/events/{run_id}` |

---

## Two close paths — why both are needed

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

**Watcher subprocess** handles local runs because terraform exits cleanly via gRPC
connection close — no signal is ever sent to the plugin process.

**Signal handler** handles CI runs because the CI runner sends SIGTERM to the
entire process group (`kill -TERM -{pgid}`). The plugin shares terraform's
process group (go-plugin sets no `Setpgid`), so it receives SIGTERM directly.
go-plugin only registers `os.Interrupt` (SIGINT) and ignores it — SIGTERM is
not intercepted by go-plugin, so our handler is always first to receive it.

**In-process goroutine** is an automatic fallback when `spawnWatcher` fails
(read-only filesystem, binary not executable, out of PIDs, etc.). It runs the
same PPID-polling logic inside the provider process, so the closed event is
always guaranteed.

`sync.Once` (`closeOnce`) ensures exactly **one** `PATCH /closed` is ever sent
regardless of which path wins the race.

---

## Why identity and git are collected in `Configure()`

`go-plugin` forwards `os.Environ()` to the plugin subprocess
(`client.go:647: cmd.Env = append(cmd.Env, os.Environ()...)`), so CI env vars
(`GITHUB_ACTIONS`, `GITHUB_RUN_ID`, `GITHUB_ACTOR`, etc.) **are available**
inside `Configure()`.

The watcher subprocess is spawned with a minimal env — only `MIT_WATCHER_MODE=1`
and `MIT_WATCHER_STATE=<path>`. It would have no CI context at all.

By collecting identity + git during `Configure()` and storing them in
`WatcherState` (persisted to disk), both the watcher subprocess and the signal
handler use the same pre-collected snapshot regardless of when they run.

---

## `MIT_WATCHER_MODE` — you never set this

This env var is set **automatically** by the provider when it spawns the watcher
subprocess. It is an internal re-execution flag.

```go
// watcher_unix.go — set by the provider, not by you
cmd.Env = []string{
    "MIT_WATCHER_MODE=1",
    "MIT_WATCHER_STATE=" + statePath,
}
```

`main.go` checks it at startup:

```go
if os.Getenv("MIT_WATCHER_MODE") == "1" {
    provider.WatcherMain()  // runs as watcher subprocess
    os.Exit(0)
}
// otherwise: normal terraform plugin serve path
```

Never set it in your shell, CI pipeline config, `.env` file, or
`terraform.tfvars`.

---

## Lock and state files

Both files live in `$TMPDIR` and are cleaned up automatically after the closed
event fires.

| File | Purpose | Content |
|---|---|---|
| `manifestit-observer-{ppid}.lock` | Idempotency — one open event per run | `"ppid:run_uuid"` |
| `manifestit-watcher-{ppid}.json` | Handoff — passes config + context to close paths | Full `WatcherState` JSON (mode `0600`) |

---

## Supported CI systems

The provider auto-detects the CI environment from standard env vars and includes
the context in `identity` of the closed event.

| CI System | Detection env var |
|---|---|
| GitHub Actions | `GITHUB_ACTIONS=true` |
| GitLab CI | `GITLAB_CI=true` |
| Jenkins | `JENKINS_URL` |
| CircleCI | `CIRCLECI=true` |
| Azure DevOps | `TF_BUILD=True` |
| Bitbucket Pipelines | `BITBUCKET_BUILD_NUMBER` |
| TeamCity | `TEAMCITY_VERSION` |
| AWS CodeBuild | `CODEBUILD_BUILD_ID` |
| Google Cloud Build | `BUILD_ID` + `PROJECT_ID` |
| Spacelift | `SPACELIFT=true` |
| Atlantis | `ATLANTIS_TERRAFORM_VERSION` |
| env0 | `ENV0_ENVIRONMENT_ID` |

---

## Test files

| File | What it tests |
|---|---|
| `internal/provider/watcher_test.go` | WatcherState serialisation, UUID generation, lock acquisition, stale lock reclaim, PPID polling |
| `pkg/sdk/providers/observer/observer_test.go` | Post (open), Patch (closed), HTTP error handling, JSON field presence |

---

## Further reading

- [`FLOW_DIAGRAM.md`](./FLOW_DIAGRAM.md) — step-by-step ASCII flow for every shutdown scenario
- [`LOCAL_TEST_GUIDE.md`](./LOCAL_TEST_GUIDE.md) — how to test locally and simulate CI/CD with `run.sh` and `ci-simulate.sh`

