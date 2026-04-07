---
page_title: "ManifestIT Provider"
subcategory: ""
description: |-
  Tracks terraform apply / destroy runs — fires an open event at start and a closed event after all resources finish.
---

# ManifestIT Provider

Automatically tracks every `terraform apply` and `terraform destroy` by firing two API events:

| Event | When |
|-------|------|
| `open` | At apply start, before any resources are touched |
| `closed` | After **all** providers finish — not just the manifestit resource |

No `depends_on` needed. The closed event fires when terraform itself exits, which only happens after every provider (AWS, GCP, etc.) has completed.

## Example Usage

```terraform
terraform {
  required_providers {
    manifestit = {
      source  = "manifest-it/manifestit"
      version = "~> 0.1"
    }
  }
}

provider "manifestit" {
  api_key                   = var.mit_api_key
  api_url                   = "https://api.manifest-it.com"
  org_id                    = 1
  org_key                   = var.mit_org_key
  provider_id               = 1
  provider_configuration_id = 1
  tracked_branch            = "main"
  tracked_repo              = "https://github.com/your-org/infra.git"
  validate                  = "true"
}

resource "manifestit_observer" "this" {}
```

## Provider Configuration

| Argument | Type | Required | Description |
|----------|------|----------|-------------|
| `api_url` | string | yes | ManifestIT API endpoint URL |
| `api_key` | string | yes | API key. Can also be set via `MIT_API_KEY` env var |
| `org_id` | number | yes | Organization ID |
| `org_key` | string | yes | Organization key |
| `provider_id` | number | yes | Provider ID |
| `provider_configuration_id` | number | yes | Provider configuration ID |
| `tracked_branch` | string | yes | Branch to track for compliance (e.g. `main`) |
| `tracked_repo` | string | yes | Git repository URL to track |
| `validate` | string | yes | Validate API key on init. Values: `true`, `false` |
| `http_client_retry_enabled` | string | no | Enable HTTP retries on 429/5xx. Default: `true` |
| `http_client_retry_max_retries` | number | no | Max retry count (1–5). Default: `3` |
