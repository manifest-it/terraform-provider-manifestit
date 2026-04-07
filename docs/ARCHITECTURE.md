# Architecture

## What it does

Fires two API events on every `terraform apply` / `destroy`:

| Event | API call | When |
|-------|----------|------|
| open | `POST /api/v1/events` | `Configure()` — before any resources |
| closed | `PATCH /api/v1/events/{run_id}` | After terraform process exits |

Both events share the same `run_id` (UUID v4).

---

## Flow

```
terraform apply
    │
    ├─► spawns provider plugin process (go-plugin)
    │       │
    │       ├─► Configure() called
    │       │       ├─ detectTerraformOperation() — reads parent cmdline
    │       │       ├─ acquireRunLock()           — atomic OS link, deduplicates
    │       │       ├─ POST /open
    │       │       ├─ spawnWatcher()             — detached subprocess (Setsid)
    │       │       └─ registerSIGTERMHandler()   — CI close path
    │       │
    │       └─► provider process exits (go-plugin SIGKILLs it after gRPC closes)
    │
    ├─► all other providers (AWS, GCP...) run in parallel
    │
    └─► terraform exits  ◄── watcher polls this (kill -0 every 2s)
            │
            └─► watcher subprocess fires PATCH /closed
```

---

## Two close paths

### 1. Watcher subprocess (local + normal CI/CD completion)

- Spawned by the provider with `Setsid: true` — runs in its own session
- Survives go-plugin's SIGKILL on the provider process
- Polls terraform PPID every 2s via `kill -0`
- Fires `PATCH /closed` when terraform exits
- Handles: local apply, CI/CD apply completing normally

### 2. SIGTERM handler (CI/CD job teardown)

- CI runners send SIGTERM to the process group when killing a job step
- Provider plugin shares terraform's PGID (go-plugin does not set Setsid)
- Handler fires `PATCH /closed` immediately, then re-raises SIGTERM
- Handles: CI runner timeout, job cancellation, pipeline abort

`providerCloseOnce` (`sync.Once`) ensures exactly one `PATCH /closed` is sent regardless of which path fires first.

---

## Deduplication

Terraform spawns the provider binary multiple times per apply (schema, plan, apply phases). Each is a separate process. The lock file at `$TMPDIR/.manifestit/observer-{ppid}.lock` uses `os.Link` (atomic on POSIX) to ensure only the first process instance fires `POST /open` and spawns the watcher.

---

## Key files

| File | Purpose |
|------|---------|
| `internal/provider/init.go` | Provider setup, `Configure()`, lock, `POST /open` |
| `internal/provider/lifecycle.go` | Watcher subprocess, SIGTERM handler, `PATCH /closed` |
| `pkg/sdk/providers/observer/observer.go` | HTTP client for open/closed API calls |
| `main.go` | Entry point — routes `MIT_WATCHER_MODE=1` to `WatcherMain()` |
</content>
</invoke>
