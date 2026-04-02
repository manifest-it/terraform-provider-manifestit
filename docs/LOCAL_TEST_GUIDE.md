# How to test with a real `terraform apply` locally

Everything lives under `localtest/`. No real ManifestIT API is needed —
a tiny mock server records every HTTP call so you can inspect events.

---

## Prerequisites

| Tool | Check |
|------|-------|
| Go ≥ 1.21 | `go version` |
| Terraform ≥ 1.1 | `terraform version` |

---

## Directory layout

```
localtest/
├── bin/                          ← built provider binary goes here
├── mock-server/main.go           ← records POST (open) and PATCH (closed) calls
├── terraform/
│   ├── main.tf                   ← minimal config pointing at localhost:8080
│   └── .terraformrc              ← dev_overrides so TF uses the local binary
├── run.sh                        ← local terraform apply end-to-end test
└── ci-simulate.sh                ← CI/CD simulation test (SIGTERM + GitHub Actions env)
```

---

## Step 1 — Build the provider binary

Run this once, and again any time you change Go code:

```bash
cd /path/to/terraform-provider-manifestit
go build -o localtest/bin/terraform-provider-manifestit .
```

---

## Step 2 — Local `terraform apply` test

### Start the mock server (Terminal 1 — leave open)

```bash
cd /path/to/terraform-provider-manifestit
go run localtest/mock-server/main.go
```

Expected:
```
mock ManifestIT API listening on http://localhost:8080
  POST  /api/v1/events        → open event
  PATCH /api/v1/events/{id}   → closed event
  GET   /dump                 → dump all received events as JSON
  POST  /reset                → clear all events
```

### Run terraform apply (Terminal 2)

```bash
cd localtest/terraform
TF_CLI_CONFIG_FILE=.terraformrc terraform apply -auto-approve
```

> **Note:** With `dev_overrides`, `terraform init` is **not needed** and will error. Go straight to `apply`.

### Verify both events

```bash
curl -s http://localhost:8080/dump | python3 -m json.tool
```

You should see:
- **Event 1:** `POST /api/v1/events` — `status:"open"` — fired immediately when `Configure()` is called
- **Event 2:** `PATCH /api/v1/events/{run_id}` — `status:"closed"` with full `identity` and `git` — fired ~2s later by the watcher subprocess when terraform exits

Both events share the same `run_id`.

### One-shot automated run

```bash
cd localtest
bash run.sh
```

---

## Step 3 — CI/CD simulation test

Simulates what GitHub Actions does: injects CI env vars, runs apply, then sends `SIGTERM` to terraform (as a CI runner would when tearing down a job step).

```bash
cd localtest
bash ci-simulate.sh
```

### What it checks

| Check | What it proves |
|---|---|
| `PASS: open event received` | Event 1 fires before SIGTERM |
| `PASS: closed event received after SIGTERM` | In-process signal handler fired the close event |
| `PASS: identity.ci_provider = 'github-actions'` | CI env vars collected in `Configure()` |
| `PASS: run_id matches` | Both events correlated by the same UUID |

---

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| `address already in use` on port 8080 | `lsof -ti :8080 \| xargs kill -9` |
| `address already in use` on port 8081 | `lsof -ti :8081 \| xargs kill -9` |
| Only open event received (no closed) | Wait 3–5s — watcher polls every 2s |
| `no available releases match` | Do **not** run `terraform init` — skip straight to `apply` |
| Binary missing | Re-run Step 1 (`go build ...`) |
| open event has no identity/git | Expected — identity/git are only in the `closed` event |

