resource "omni_cluster" "example" {
  name               = "example"
  kubernetes_version = "1.36.2"
  talos_version      = "1.13.5"

  backup_interval = "1h"

  features = {
    disk_encryption       = true
    enable_workload_proxy = true
  }
}
