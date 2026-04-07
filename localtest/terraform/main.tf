terraform {
  required_providers {
    manifestit = {
      source = "registry.terraform.io/manifest-it/manifestit"
    }
    aws = {
      source = "hashicorp/aws"
    }
  }
}

provider "aws" {
  region = "us-east-1"
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

# -------------------------------
# Infra module (ALL resources)
# -------------------------------

module "infra" {
  source = "./infra"
}

# -------------------------------
# FINAL observer (clean)
# -------------------------------

resource "manifestit_observer" "final" {
}