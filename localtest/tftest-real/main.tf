##############################################################################
# Real-environment test — pointing to localhost:3000
##############################################################################

terraform {
  required_providers {
    manifestit = {
      source  = "registry.terraform.io/manifest-it/manifestit"
      version = ">= 0.1.0"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.0"
    }
  }
}

provider "manifestit" {
  api_key                   = "local-dev-key"
  api_url                   = "http://localhost:8080"
  org_id                    = 1
  org_key                   = "local-dev-org"
  provider_id               = 1
  provider_configuration_id = 1
  tracked_branch            = "main"
  tracked_repo              = "https://github.com/manifest-it/terraform-provider-manifestit.git"
  validate                  = "false"
}

# Simulates a slow resource (AWS S3, EC2, RDS etc.) taking 10s.
# Proves PATCH /closed does NOT fire before this completes.
resource "null_resource" "slow_resource" {
  triggers = {
    run = timestamp()
  }
  provisioner "local-exec" {
    command = "echo 'Slow resource started...' && sleep 10 && echo 'Slow resource done'"
  }
}

# No depends_on needed — PATCH /closed fires when terraform (PPID) exits,
# which only happens after ALL providers finish.
resource "manifestit_observer" "this" {}

output "test_summary" {
  value = "Apply complete. Check localhost:8080 for open+closed events."
}
