resource "omni_cluster" "example" {
  name               = "example"
  talos_version      = "1.14.5"
  kubernetes_version = "1.37.1"
}

resource "omni_machine_set" "control_plane" {
  cluster = omni_cluster.example.name
  role    = "controlplane"
}

resource "omni_machine_set_node" "control_plane" {
  for_each = toset(var.control_plane_machine_ids)

  cluster     = omni_cluster.example.name
  machine_set = omni_machine_set.control_plane.id
  machine_id  = each.value
}

# Omni brings the cluster up after Terraform has created the resources above. This waits until it
# is up: machine sets running, every machine healthy, control plane and Kubernetes API ready.
resource "omni_cluster_wait" "example" {
  cluster = omni_cluster.example.name

  # The machines the cluster should end up with. Referencing the nodes also makes this wait start
  # once they all exist, and a changed set of machines waits again, including for removed machines
  # to be taken out.
  machine_ids = [for node in omni_machine_set_node.control_plane : node.machine_id]

  # Wait again after an upgrade, too.
  triggers = {
    talos_version      = omni_cluster.example.talos_version
    kubernetes_version = omni_cluster.example.kubernetes_version
  }

  timeouts = {
    create = "45m"
  }
}

# Anything that needs a working cluster depends on the wait.
output "cluster_ready" {
  value = omni_cluster_wait.example.ready
}
