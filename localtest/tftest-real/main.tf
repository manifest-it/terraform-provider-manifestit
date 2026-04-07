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
    random = {
      source  = "hashicorp/random"
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

# Simulates slow resources (AWS S3, EC2, RDS etc.) running in parallel.
# 10s sleep — closed event must NOT fire before this completes.
resource "null_resource" "heartbeat_test_delay" {
  triggers = {
    run = "test-1"
  }
  provisioner "local-exec" {
    command = "echo 'Simulating slow resource (10s)...' && sleep 10 && echo 'Slow resource done'"
  }
}

resource "random_id" "bucket_suffix" {
  byte_length = 8
}

# NO depends_on — watcher subprocess fires close after terraform fully exits
resource "manifestit_observer" "this" {}

output "test_summary" {
  value = "Apply complete. Check localhost:3000 for open+closed events."
}
