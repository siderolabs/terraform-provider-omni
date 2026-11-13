variable "cluster_name" {
  description = "The Omni cluster name. Also prefixes the VM names."
  type        = string
}

variable "talos_version" {
  description = "The Talos version of the cluster, without the leading `v`."
  type        = string
}

variable "kubernetes_version" {
  description = "The Kubernetes version of the cluster, without the leading `v`."
  type        = string
}

variable "control_plane_count" {
  description = "How many control plane VMs to create."
  type        = number
  default     = 1
}

variable "worker_count" {
  description = "How many worker VMs to create."
  type        = number
  default     = 1
}

variable "control_plane" {
  description = "Size of the control plane VMs."
  type = object({
    cores     = optional(number, 2)
    memory    = optional(number, 2048)
    disk_size = optional(number, 10)
  })
  default = {}
}

variable "worker" {
  description = "Size of the worker VMs."
  type = object({
    cores     = optional(number, 2)
    memory    = optional(number, 2048)
    disk_size = optional(number, 10)
  })
  default = {}
}

variable "node_name" {
  description = "The Proxmox node to create the VMs on."
  type        = string
}

variable "talos_iso_file_id" {
  description = "The Talos `nocloud` ISO to boot, as a Proxmox volume ID such as `local:iso/talos-nocloud-amd64.iso`. A stock image is enough: Omni installs the cluster's schematic once the machine has joined."
  type        = string
}

variable "bridge" {
  description = "The bridge to attach the VMs to. It needs DHCP and a route to Omni."
  type        = string
  default     = "vmbr0"
}

variable "disk_datastore_id" {
  description = "The datastore for the VM disks and cloud-init drives."
  type        = string
  default     = "local"
}

variable "snippets_datastore_id" {
  description = "The datastore to upload the cloud-init user data to. It must have the `snippets` content type enabled."
  type        = string
  default     = "local"
}

variable "join_token_id" {
  description = "The Omni join token the machines join with. Null uses the instance default."
  type        = string
  default     = null
  sensitive   = true
}

variable "ready_timeout" {
  description = "How long to wait for the cluster to become ready once the machines have joined."
  type        = string
  default     = "30m"
}

variable "qemu_guest_agent" {
  description = "Give the VMs a QEMU guest agent channel. Leave it on when the Talos image includes the `qemu-guest-agent` extension, which Talos otherwise waits on forever, keeping the machines from ever becoming healthy. Turn it off for an image without the extension."
  type        = bool
  default     = true
}
