---
page_title: "manifestit_observer Resource - ManifestIT"
subcategory: ""
description: |-
  Activates the ManifestIT observer for Terraform operation tracking.
---

# manifestit_observer (Resource)

Activates the ManifestIT observer. This is a no-op resource that triggers the provider's data collection and posting on every Terraform operation (apply, destroy).

The provider automatically collects and posts identity, git context, and operational metadata when this resource is present in the configuration.

## Example Usage

```terraform
resource "manifestit_observer" "this" {}
```

## Schema

### Read-Only

- `id` (String) - Static identifier for the observer resource.
