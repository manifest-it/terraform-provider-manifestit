terraform {
  required_providers {
    manifestit = {
      source = "registry.terraform.io/manifest-it/manifestit"
    }
    time = {
      source  = "hashicorp/time"
      version = "~> 0.9"
    }
  }
}

provider "manifestit" {
  api_key                   = "local-dev-key"
  api_url                   = "http://localhost:8080"
  validate                  = "false"
  org_id                    = 1
  org_key                   = "local-dev-org"
  provider_id               = 1
  provider_configuration_id = 1
  tracked_branch            = "main"
  tracked_repo              = "https://github.com/manifest-it/terraform-provider-manifestit.git"
}

# Simulates a slow "other provider" resource (aws_instance, aws_db_instance etc).
# Takes 10s to create — proves the closed event does NOT fire early.
# No real AWS credentials needed.
resource "time_sleep" "simulate_slow_aws" {
  create_duration = "10s"
}

# NO depends_on — the closed event fires when the terraform binary exits,
# which only happens after ALL providers (time + manifestit) are done.
resource "manifestit_observer" "this" {}
