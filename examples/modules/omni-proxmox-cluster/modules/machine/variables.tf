variable "name" {
  description = "The VM name. Also the base of the label that identifies the machine in Omni."
  type        = string
}

variable "node_name" {
  description = "The Proxmox node to create the VM on."
  type        = string
}

variable "talos_iso_file_id" {
  description = "The Talos ISO to boot, as a Proxmox volume ID such as `local:iso/talos-nocloud-amd64.iso`. It must be a `nocloud` image, which reads the join config from the cloud-init drive."
  type        = string
}

variable "join_token_id" {
  description = "The Omni join token to join with. Null uses the instance default."
  type        = string
  default     = null
  sensitive   = true
}

variable "labels" {
  description = "Extra labels to put on the machine in Omni when it joins."
  type        = map(string)
  default     = {}
}

variable "machine_label_key" {
  description = "The label key whose value is unique to this machine, used to find it in Omni once it has joined."
  type        = string
  default     = "terraform/machine"
}

variable "bridge" {
  description = "The bridge to attach the VM's network interface to. Talos takes its address over DHCP."
  type        = string
  default     = "vmbr0"
}

variable "disk_datastore_id" {
  description = "The datastore for the VM disk and the cloud-init drive."
  type        = string
  default     = "local"
}

variable "snippets_datastore_id" {
  description = "The datastore to upload the cloud-init user data to. It must have the `snippets` content type enabled."
  type        = string
  default     = "local"
}

variable "cores" {
  description = "CPU cores."
  type        = number
  default     = 2
}

variable "memory" {
  description = "Memory in MiB. Talos needs at least 2048 for a control plane node."
  type        = number
  default     = 2048
}

variable "disk_size" {
  description = "Disk size in GiB."
  type        = number
  default     = 10
}

variable "join_timeout" {
  description = "How long to wait for the machine to join Omni."
  type        = string
  default     = "15m"
}

variable "qemu_guest_agent" {
  description = "Give the VM a QEMU guest agent channel. Leave it on when the Talos image includes the `qemu-guest-agent` extension, which Talos otherwise waits on forever, keeping the machine from ever becoming healthy. Turn it off for an image without the extension."
  type        = bool
  default     = true
}

variable "disk_aio" {
  description = "The disk's asynchronous I/O mode: `io_uring`, `native` or `threads`. Use `threads` on a host that disables io_uring, where QEMU would otherwise fail to start the VM."
  type        = string
  default     = "io_uring"
}
