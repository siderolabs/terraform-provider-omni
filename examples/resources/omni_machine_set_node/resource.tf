resource "omni_cluster" "example" {
  name               = "example"
  kubernetes_version = "1.36.2"
  talos_version      = "1.13.5"
}

resource "omni_machine_set" "control_planes" {
  cluster = omni_cluster.example.name
  role    = "controlplane"
}

# Explicitly assign a machine (by UUID) to the control plane machine set.
resource "omni_machine_set_node" "cp0" {
  machine_id  = "430d882a-51a8-48b3-ae00-90c5b0b5b0b0"
  machine_set = omni_machine_set.control_planes.name
  cluster     = omni_cluster.example.name
}
