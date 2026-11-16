# Uses the module against a Proxmox host and an Omni instance.
#
#   export OMNI_ENDPOINT=https://omni.example.com
#   export OMNI_SERVICE_ACCOUNT_KEY=...
#   export PROXMOX_VE_PASSWORD=...
#   terraform apply

terraform {
  required_providers {
    omni = {
      source = "siderolabs/omni"
    }
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.80.0"
    }
  }
}

variable "proxmox_endpoint" {
  type    = string
  default = "https://localhost:8006/"
}

variable "proxmox_ssh_address" {
  description = "Where to reach the Proxmox node over SSH, which is how snippets are uploaded."
  type        = string
  default     = "127.0.0.1"
}

variable "proxmox_ssh_port" {
  type    = number
  default = 22
}

variable "node_name" {
  type    = string
  default = "pve-1"
}

variable "talos_iso_file_id" {
  type = string
}

variable "bridge" {
  type    = string
  default = "vmbr0"
}

provider "omni" {
  insecure_skip_tls_verify = true
}

provider "proxmox" {
  endpoint = var.proxmox_endpoint
  username = "root@pam"
  # password is read from PROXMOX_VE_PASSWORD.
  insecure = true

  # Proxmox's API cannot upload snippets, so the provider copies them over SSH.
  ssh {
    agent    = false
    username = "root"
    # password defaults to the API password.

    node {
      name    = var.node_name
      address = var.proxmox_ssh_address
      port    = var.proxmox_ssh_port
    }
  }
}

module "cluster" {
  source = "../"

  cluster_name       = "proxmox-demo"
  talos_version      = "1.14.5"
  kubernetes_version = "1.37.1"

  control_plane_count = 1
  worker_count        = 1

  node_name         = var.node_name
  talos_iso_file_id = var.talos_iso_file_id
  bridge            = var.bridge
}

output "cluster_name" {
  value = module.cluster.cluster_name
}

output "machines" {
  value = merge(module.cluster.control_plane_machine_ids, module.cluster.worker_machine_ids)
}
