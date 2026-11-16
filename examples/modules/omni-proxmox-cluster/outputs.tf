output "cluster_name" {
  description = "The Omni cluster name. Only known once the cluster is ready, so anything using it waits for that."
  value       = omni_cluster_wait.this.cluster
}

output "control_plane_machine_ids" {
  description = "The Omni machine UUIDs of the control plane nodes, by VM name."
  value       = { for name, m in module.control_plane : name => m.machine_id }
}

output "worker_machine_ids" {
  description = "The Omni machine UUIDs of the worker nodes, by VM name."
  value       = { for name, m in module.worker : name => m.machine_id }
}
