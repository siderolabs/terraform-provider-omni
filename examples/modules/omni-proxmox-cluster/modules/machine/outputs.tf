output "machine_id" {
  description = "The UUID of the machine in Omni."
  value       = omni_machine_wait.this.machine_id
}

output "vm_id" {
  description = "The Proxmox VM ID."
  value       = proxmox_virtual_environment_vm.this.vm_id
}

output "machine_label" {
  description = "The label value that identifies this machine in Omni."
  value       = local.machine_label
}

output "ipv4_addresses" {
  description = "The VM's IPv4 addresses as the guest agent reports them. Empty without the agent."
  value       = flatten(proxmox_virtual_environment_vm.this.ipv4_addresses)
}
