---
page_title: "manifestit_observer Resource - ManifestIT"
subcategory: ""
description: |-
  No-op resource that activates the ManifestIT observer lifecycle.
---

# manifestit_observer (Resource)

A no-op resource whose only purpose is to trigger the provider `Configure()` call, which fires the `open` event and starts the watcher subprocess.

The `closed` event fires when terraform itself exits — after **all** resources across all providers finish. No `depends_on` required.

## Example Usage

```terraform
resource "manifestit_observer" "this" {}
```

## Schema

### Read-Only

- `id` (String) — Static identifier, always `"manifestit-observer"`.
