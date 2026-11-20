# Label a machine, for machine classes and label selectors to match it by. Only these keys are
# managed: the labels the machine joined with stay as they are.
resource "omni_machine_labels" "worker_0" {
  machine_id = omni_machine_wait.worker_0.machine_id

  labels = {
    "role" = "worker"
    "zone" = "eu-central-1a"
  }
}

# Machine classes select machines by their labels.
resource "omni_machine_class" "eu_workers" {
  name         = "eu-workers"
  match_labels = ["role = worker, zone = eu-central-1a"]
}
