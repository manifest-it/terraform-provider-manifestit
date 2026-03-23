---
page_title: "ManifestIT Provider"
subcategory: ""
description: |-
  The ManifestIT provider collects and reports observability data for Terraform operations.
---

# ManifestIT Provider

The ManifestIT provider automatically collects identity, git context, and operational metadata during Terraform runs, posting this data to the ManifestIT platform for compliance and audit tracking.

## Example Usage

```terraform
terraform {
  required_providers {
    manifestit = {
      source  = "manifest-it/manifestit"
      version = "0.1.0"
    }
  }
}

provider "manifestit" {
  api_key                   = "your-api-key"
  api_url                   = "https://api.manifestit.io"
  org_id                    = 1
  org_key                   = "your-org-key"
  provider_id               = 1
  provider_configuration_id = 1
  tracked_branch            = "main"
  tracked_repo              = "https://github.com/your-org/your-repo.git"
}

resource "manifestit_observer" "this" {}
```

## Authentication

The provider requires an API key for authentication. It can be set directly in the provider block or via the `MIT_API_KEY` environment variable.

```terraform
provider "manifestit" {
  api_key = "your-api-key"
  # ... other configuration
}
```

Or using an environment variable:

```shell
export MIT_API_KEY="your-api-key"
```

## Schema

### Required

- `api_url` (String) - ManifestIT API endpoint URL.
- `org_id` (Number) - Organization identifier.
- `org_key` (String) - Organization key.
- `provider_id` (Number) - Provider identifier.
- `provider_configuration_id` (Number) - Provider configuration identifier.
- `tracked_branch` (String) - Git branch to track for merge ancestry checks.
- `tracked_repo` (String) - Git repository URL to validate against.
- `validate` (String) - Enable or disable API key validation (`"true"` or `"false"`).

### Optional

- `api_key` (String, Sensitive) - ManifestIT API key. Can also be set via the `MIT_API_KEY` environment variable.
- `http_client_retry_enabled` (String) - Enable HTTP client retry (`"true"` or `"false"`). Defaults to `"true"`. Can also be set via `MIT_HTTP_RETRY_ENABLED`.
- `http_client_retry_max_retries` (Number) - Maximum number of HTTP retries (1-5). Can also be set via `MIT_HTTP_RETRY_MAX_RETRIES`.
