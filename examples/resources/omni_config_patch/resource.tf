resource "omni_cluster" "example" {
  name               = "example"
  kubernetes_version = "1.36.2"
  talos_version      = "1.13.5"
}

# A cluster-wide config patch.
resource "omni_config_patch" "kubelet" {
  name    = "kubelet-extra-args"
  weight  = 400
  cluster = omni_cluster.example.name

  data = yamlencode({
    machine = {
      kubelet = {
        extraArgs = {
          "max-pods" = "150"
        }
      }
    }
  })
}

# A patch narrowed to a single machine set of the cluster.
resource "omni_config_patch" "workers_kubelet" {
  name    = "workers-kubelet-extra-args"
  cluster = omni_cluster.example.name

  selector = {
    machine_set = "example-workers"
  }

  data = yamlencode({
    machine = {
      kubelet = {
        extraArgs = {
          "max-pods" = "200"
        }
      }
    }
  })
}

# A patch targeting a single machine that is not part of a cluster.
resource "omni_config_patch" "standalone" {
  name = "standalone-install-disk"

  selector = {
    machine = "e8b8e0f0-1111-2222-3333-444455556666"
  }

  data = yamlencode({
    machine = {
      install = {
        disk = "/dev/sda"
      }
    }
  })
}
