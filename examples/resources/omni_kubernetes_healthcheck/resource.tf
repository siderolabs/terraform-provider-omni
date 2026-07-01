resource "omni_cluster" "example" {
  name               = "example"
  kubernetes_version = "1.36.2"
  talos_version      = "1.13.5"
}

resource "omni_kubernetes_healthcheck" "dns" {
  name     = "dns-ready"
  cluster  = omni_cluster.example.name
  interval = "30s"

  job = yamlencode({
    apiVersion = "batch/v1"
    kind       = "Job"
    metadata = {
      name      = "dns-ready"
      namespace = "kube-system"
    }
    spec = {
      template = {
        spec = {
          restartPolicy = "Never"
          containers = [{
            name    = "check"
            image   = "busybox"
            command = ["nslookup", "kubernetes.default.svc.cluster.local"]
          }]
        }
      }
    }
  })
}
