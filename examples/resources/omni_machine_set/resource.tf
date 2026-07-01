resource "omni_cluster" "example" {
  name               = "example"
  kubernetes_version = "1.36.2"
  talos_version      = "1.13.5"
}

# Control plane machine set. A cluster may have only one.
resource "omni_machine_set" "control_planes" {
  cluster = omni_cluster.example.name
  role    = "controlplane"
}

# Worker machine set that automatically allocates machines from a machine class.
resource "omni_machine_set" "workers" {
  cluster = omni_cluster.example.name
  role    = "workers"

  update_strategy = {
    type            = "Rolling"
    max_parallelism = 2
  }

  machine_class = {
    name = "my-machine-class"
    size = 3
  }
}

# Extra workers.
resource "omni_machine_set" "extra-workers" {
  name    = "extra-workers"
  cluster = omni_cluster.example.name
  role    = "workers"

  update_strategy = {
    type            = "Rolling"
    max_parallelism = 2
  }

  machine_class = {
    name = "my-machine-class"
    size = 3
  }
}
