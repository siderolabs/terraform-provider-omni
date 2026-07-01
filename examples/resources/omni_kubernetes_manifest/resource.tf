resource "omni_cluster" "example" {
  name               = "example"
  kubernetes_version = "1.36.2"
  talos_version      = "1.13.5"
}

resource "omni_kubernetes_manifest" "namespace" {
  name    = "team-namespace"
  weight  = 400
  mode    = "full"
  cluster = omni_cluster.example.name

  data = yamlencode({
    apiVersion = "v1"
    kind       = "Namespace"
    metadata = {
      name = "team"
    }
  })
}
