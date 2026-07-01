resource "omni_cluster" "example" {
  name               = "example"
  kubernetes_version = "1.36.2"
  talos_version      = "1.13.5"
}

resource "omni_machine_set" "workers" {
  cluster = omni_cluster.example.name
  role    = "workers"
}

# Extensions installed on every machine of the cluster.
resource "omni_machine_extensions" "cluster" {
  cluster = omni_cluster.example.name

  extensions = [
    "siderolabs/util-linux-tools",
  ]
}

# Extensions installed only on the machines of a single machine set.
resource "omni_machine_extensions" "workers" {
  cluster = omni_cluster.example.name

  selector = {
    machine_set = omni_machine_set.workers.name
  }

  extensions = [
    "siderolabs/iscsi-tools",
    "siderolabs/util-linux-tools",
  ]
}

# Extensions installed on a single cluster machine (by machine UUID).
resource "omni_machine_extensions" "single" {
  cluster = omni_cluster.example.name

  selector = {
    cluster_machine = "e8b8e0f0-1111-2222-3333-444455556666"
  }

  extensions = [
    "siderolabs/gvisor",
  ]
}
