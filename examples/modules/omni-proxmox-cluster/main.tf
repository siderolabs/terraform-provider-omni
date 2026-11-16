# An Omni cluster on Proxmox VMs, from nothing to a ready Kubernetes cluster in one apply:
#
#   1. each VM boots a stock Talos ISO with Omni's join config as its cloud-init user data,
#   2. `omni_machine_wait` waits for it to join Omni and resolves its UUID from a label the join
#      config carries,
#   3. the machines are put into the cluster's machine sets,
#   4. `omni_cluster_wait` waits for Omni to report the cluster ready.

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    omni = {
      source = "siderolabs/omni"
    }
    proxmox = {
      source  = "bpg/proxmox"
      version = ">= 0.80.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.0.0"
    }
  }
}

locals {
  control_planes = { for i in range(var.control_plane_count) : "${var.cluster_name}-cp-${i}" => i }
  workers        = { for i in range(var.worker_count) : "${var.cluster_name}-worker-${i}" => i }

  machine_defaults = {
    node_name             = var.node_name
    talos_iso_file_id     = var.talos_iso_file_id
    bridge                = var.bridge
    disk_datastore_id     = var.disk_datastore_id
    snippets_datastore_id = var.snippets_datastore_id
  }
}

module "control_plane" {
  source   = "./modules/machine"
  for_each = local.control_planes

  name                  = each.key
  node_name             = local.machine_defaults.node_name
  talos_iso_file_id     = local.machine_defaults.talos_iso_file_id
  bridge                = local.machine_defaults.bridge
  disk_datastore_id     = local.machine_defaults.disk_datastore_id
  snippets_datastore_id = local.machine_defaults.snippets_datastore_id
  qemu_guest_agent      = var.qemu_guest_agent
  join_token_id         = var.join_token_id
  cores                 = var.control_plane.cores
  memory                = var.control_plane.memory
  disk_size             = var.control_plane.disk_size

  labels = {
    "terraform/cluster" = var.cluster_name
    "terraform/role"    = "controlplane"
  }
}

module "worker" {
  source   = "./modules/machine"
  for_each = local.workers

  name                  = each.key
  node_name             = local.machine_defaults.node_name
  talos_iso_file_id     = local.machine_defaults.talos_iso_file_id
  bridge                = local.machine_defaults.bridge
  disk_datastore_id     = local.machine_defaults.disk_datastore_id
  snippets_datastore_id = local.machine_defaults.snippets_datastore_id
  qemu_guest_agent      = var.qemu_guest_agent
  join_token_id         = var.join_token_id
  cores                 = var.worker.cores
  memory                = var.worker.memory
  disk_size             = var.worker.disk_size

  labels = {
    "terraform/cluster" = var.cluster_name
    "terraform/role"    = "worker"
  }
}

resource "omni_cluster" "this" {
  name               = var.cluster_name
  talos_version      = var.talos_version
  kubernetes_version = var.kubernetes_version
}

resource "omni_machine_set" "control_plane" {
  cluster = omni_cluster.this.name
  role    = "controlplane"
}

resource "omni_machine_set" "workers" {
  cluster = omni_cluster.this.name
  role    = "workers"
}

resource "omni_machine_set_node" "control_plane" {
  for_each = module.control_plane

  cluster     = omni_cluster.this.name
  machine_set = omni_machine_set.control_plane.id
  machine_id  = each.value.machine_id
}

resource "omni_machine_set_node" "worker" {
  for_each = module.worker

  cluster     = omni_cluster.this.name
  machine_set = omni_machine_set.workers.id
  machine_id  = each.value.machine_id
}

resource "omni_cluster_wait" "this" {
  cluster = omni_cluster.this.name

  # Exactly these machines, so adding or removing one waits until Omni has finished doing so.
  machine_ids = concat(
    values(omni_machine_set_node.control_plane)[*].machine_id,
    values(omni_machine_set_node.worker)[*].machine_id,
  )

  # Wait again after an upgrade, too.
  triggers = {
    talos_version      = omni_cluster.this.talos_version
    kubernetes_version = omni_cluster.this.kubernetes_version
  }

  timeouts = {
    create = var.ready_timeout
  }
}
