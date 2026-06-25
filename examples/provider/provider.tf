terraform {
  required_providers {
    omni = {
      source = "siderolabs/omni"
    }
  }
}

provider "omni" {
  endpoint = "https://instance.omni.siderolabs.io"
  # service_account_key is sourced from the OMNI_SERVICE_ACCOUNT_KEY environment variable.
}
