# One Proxmox VM that boots a stock Talos ISO, registers with Omni, and is waited for until it has.
#
# Omni names a machine by its UUID, which is not known until the machine has joined. To tell which
# Omni machine is this VM, the join config carries a label unique to it: Omni signs the label into the
# join token and puts it on the machine when it registers, and `omni_machine_wait` waits for the
# machine carrying it.

terraform {
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

# Keeps the label unique beyond this configuration, so two workspaces using the same names do not
# claim each other's machines.
resource "random_id" "machine" {
  byte_length = 4
}

locals {
  machine_label = "${var.name}-${random_id.machine.hex}"

  labels = merge(var.labels, {
    (var.machine_label_key) = local.machine_label
  })
}

data "omni_machine_join_config" "this" {
  token_id       = var.join_token_id
  machine_labels = local.labels
}

# Proxmox's cloud-init drive takes custom user data only as a snippet file on a datastore. Talos's
# nocloud platform reads the user data as its machine config, which is all the join config is.
resource "proxmox_virtual_environment_file" "user_data" {
  content_type = "snippets"
  datastore_id = var.snippets_datastore_id
  node_name    = var.node_name

  source_raw {
    file_name = "omni-${local.machine_label}.yaml"
    data      = data.omni_machine_join_config.this.config
  }
}

resource "proxmox_virtual_environment_vm" "this" {
  name      = var.name
  node_name = var.node_name
  tags      = ["omni", "talos"]

  # The VM is thrown away on destroy, so there is no point waiting for a graceful shutdown.
  stop_on_destroy = true

  # The agent channel must match the image. With the qemu-guest-agent extension in the image and no
  # channel, the extension waits for its device forever, so Talos never reports the machine healthy
  # and the cluster never becomes ready. With the channel and no extension, the provider waits for
  # the agent until `timeout` on every create.
  agent {
    enabled = var.qemu_guest_agent
    trim    = var.qemu_guest_agent
    timeout = "5m"
  }

  cpu {
    cores = var.cores
    type  = "x86-64-v2-AES"
  }

  memory {
    dedicated = var.memory
  }

  operating_system {
    type = "l26"
  }

  scsi_hardware = "virtio-scsi-single"

  disk {
    datastore_id = var.disk_datastore_id
    interface    = "scsi0"
    size         = var.disk_size
    file_format  = "raw"
    iothread     = true
    aio          = var.disk_aio
  }

  # The ISO boots Talos in maintenance mode, Omni installs it to the disk, and from then on the VM
  # boots from the disk, which is why the disk comes first even though it starts out empty.
  cdrom {
    file_id   = var.talos_iso_file_id
    interface = "ide3"
  }

  boot_order = ["scsi0", "ide3"]

  network_device {
    bridge = var.bridge
    model  = "virtio"
  }

  initialization {
    datastore_id      = var.disk_datastore_id
    interface         = "ide2"
    user_data_file_id = proxmox_virtual_environment_file.user_data.id

    ip_config {
      ipv4 {
        address = "dhcp"
      }
    }
  }
}

resource "omni_machine_wait" "this" {
  labels = {
    (var.machine_label_key) = local.machine_label
  }

  # A replaced VM is a new machine, which has to be waited for again.
  triggers = {
    vm_id = proxmox_virtual_environment_vm.this.id
  }

  # The VM goes away with this resource, so drop it from Omni too rather than leaving a
  # disconnected machine behind.
  remove_on_destroy = true

  timeouts = {
    create = var.join_timeout
  }
}
