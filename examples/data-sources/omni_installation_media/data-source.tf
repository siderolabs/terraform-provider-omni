# The media parameters: what goes into the image.
resource "omni_installation_media_preset" "metal" {
  name          = "metal-production"
  architecture  = "amd64"
  talos_version = "1.13.5"

  extensions = ["siderolabs/qemu-guest-agent"]

  machine_labels = {
    env = "production"
  }
}

# Resolve the preset into something that can actually be fetched. Omni registers the schematic with
# the image factory and assembles the URL, so nothing here encodes the factory's layout.
data "omni_installation_media" "iso" {
  preset = omni_installation_media_preset.metal.name
  format = "iso"

  # Proxmox fetches the file itself, from a queue, so the URL has to still work by the time that
  # runs rather than only while Terraform is applying.
  download_token_ttl = "1h"
}

# Pull the ISO into a Proxmox datastore.
#
# file_name is keyed on storage_key, not on the URL: against an authenticated image factory Omni
# mints a fresh download token on every read, so the URL changes every plan while the medium it
# points at does not. Naming the file after the URL would re-download the same ISO forever.
#
# ISO is the simplest route: the VM boots it and Talos comes up in maintenance mode.
resource "proxmox_virtual_environment_download_file" "talos" {
  content_type = "iso"
  datastore_id = "local"
  node_name    = "pve"

  url       = data.omni_installation_media.iso.url
  file_name = "talos-${substr(data.omni_installation_media.iso.storage_key, 0, 16)}.iso"
}

resource "proxmox_virtual_environment_vm" "worker" {
  count     = 3
  node_name = "pve"

  cdrom {
    file_id = proxmox_virtual_environment_download_file.talos.id
  }
}

# For bare metal, the same preset yields an iPXE script URL instead.
data "omni_installation_media" "pxe" {
  preset = omni_installation_media_preset.metal.name
  format = "pxe"
}

# Importing a disk image instead of booting an ISO.
#
# `raw.zst` rather than `raw` (which is a shorthand for `raw.xz`): this resource decompresses gz,
# lzo, zst and bz2, but not xz. The image factory compresses to whichever suffix is asked for, so
# zstd costs nothing and is what Talos itself defaults to from 1.8 onward.
data "omni_installation_media" "disk" {
  preset = omni_installation_media_preset.metal.name
  format = "raw.zst"

  download_token_ttl = "1h"
}

resource "proxmox_virtual_environment_download_file" "talos_disk" {
  content_type            = "import"
  datastore_id            = "local"
  node_name               = "pve"
  decompression_algorithm = "zst"

  url       = data.omni_installation_media.disk.url
  file_name = "talos-${substr(data.omni_installation_media.disk.storage_key, 0, 16)}.raw"
}

resource "proxmox_virtual_environment_vm" "worker_from_disk" {
  node_name = "pve"

  disk {
    datastore_id = "local"
    import_from  = proxmox_virtual_environment_download_file.talos_disk.id
    interface    = "scsi0"
  }
}
