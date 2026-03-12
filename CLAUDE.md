# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Terraform provider for the ManifestIT service, built on the HashiCorp Terraform Plugin Framework v1.18. The provider registry address is `registry.terraform.io/manifestit/manifestit`.

## Build & Run Commands

```bash
# Build the provider binary
go build -o terraform-provider-manifestit

# Run with debug mode (for Delve debugger attachment)
go run main.go -debug

# Run all tests (none exist yet)
go test ./...

# Run a single test
go test ./internal/resource/ -run TestFunctionName -v

# Vet and lint
go vet ./...
```

## Architecture

### Entry Point

`main.go` registers the provider via `providerserver.Serve()` and supports a `-debug` flag for debugger integration.

### Provider Configuration (`internal/provider/init.go`)

The provider accepts these configuration attributes (all overridable via `MIT_*` environment variables):
- `api_key` / `MIT_API_KEY` — ManifestIT API key (sensitive)
- `api_url` / `MIT_HOST` — API endpoint URL
- `org_id` / `MIT_ORG_ID` — Organization identifier
- `validate` — Enable/disable API key validation
- `http_client_retry_*` — Configurable retry behavior (enabled, timeout, backoff multiplier/base, max retries)

### Two-Layer Structure

- **`internal/`** — Terraform-specific code (provider setup, resources, data sources). This is where Terraform schema definitions and CRUD operations live.
- **`pkg/sdk/`** — Reusable SDK for ManifestIT API interactions, independent of Terraform.

### SDK Packages (`pkg/sdk/`)

- **`http.go`** — `HTTPExecutor` with automatic retry (exponential backoff + jitter), auth strategy injection, request/response logging, and HTTP tracing. Retries on 429, 408, 502, 503, 504, and general 5xx.
- **`auth/`** — Strategy pattern for authentication: `APIKeyAuth`, `BearerTokenAuth`, `BasicAuth`, `OAuth2TokenAuth` (thread-safe auto-refresh), `CustomHeaderAuth`, `MultiAuth`, `NoAuth`. OAuth2 client credentials flow in `oauth2.go`.
- **`errors/`** — `HTTPError` struct and sentinel errors (`ErrUnauthorized`, `ErrRateLimited`, `ErrNotFound`, etc.) with status code classification via `Handle()`.
- **`stream/`** — Generic `Do()` function for batch-streaming items to a channel.
- **`providers/`** — API client implementations per ManifestIT domain (e.g., `changelog/`).

### Utilities (`pkg/utils/`)

- Generic `Retry[T]()` with exponential backoff, jitter, and context cancellation
- `GetMultiEnvVar()` for fallback environment variable resolution
- All `MIT_*` env var constants defined here

### Resources

- **`manifestit_changelog`** (`internal/resource/changelog.go`) — Currently a stub implementation with ID generation and name field, no API calls yet.

## Key Patterns

- **Auth strategy pattern**: New auth methods implement `AuthStrategy` interface with `Apply(*http.Request) error`.
- **Provider config → SDK client**: Provider `Configure()` builds an SDK client that resources use for API calls.
- **Structured logging**: Uses `zerolog` throughout the SDK layer.
