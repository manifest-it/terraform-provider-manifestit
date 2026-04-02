terraform {
  required_providers {
    manifestit = { source = "registry.terraform.io/manifest-it/manifestit" }
  }
}
provider "manifestit" {
  api_key                   = "ci-test-key"
  api_url                   = "http://localhost:8081"
  validate                  = "false"
  org_id                    = 1
  org_key                   = "ci-org"
  provider_id               = 1
  provider_configuration_id = 1
  tracked_branch            = "main"
  tracked_repo              = "https://github.com/manifest-it/terraform-provider-manifestit.git"
}
resource "manifestit_observer" "this" {}
