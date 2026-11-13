resource "omni_join_token" "fleet" {
  name       = "fleet"
  expiration = "2027-01-01T00:00:00Z"
}

# What a machine needs in order to register itself with Omni. The partial machine configuration and
# the kernel arguments are two ways of delivering the same thing, so pick whichever the platform
# booting the machine accepts.
data "omni_machine_join_config" "fleet" {
  token_id = omni_join_token.fleet.id
}

# Selecting the token by name instead keeps the secret out of the configuration entirely. Use
# `initial` for the token Omni bootstrapped itself with.
data "omni_machine_join_config" "by_name" {
  token_name = "fleet"

  depends_on = [omni_join_token.fleet]
}

# With neither selector, the instance default is used, which `omni_default_join_token` sets.
data "omni_machine_join_config" "default" {}

# A machine cannot reach Omni's SideroLink UDP port from everywhere. Behind a proxy or a firewall
# that passes only HTTPS, tunnel the WireGuard traffic over gRPC instead. It costs throughput, so
# leave it off where plain UDP works.
data "omni_machine_join_config" "tunnelled" {
  token_id        = omni_join_token.fleet.id
  use_grpc_tunnel = true
}

# --- Cloud: a stock Talos image plus the join config as user data ---------------------------------
#
# No Omni-specific installation media is needed here. The machine boots a stock Talos image, the
# join config makes it register, and Omni installs whatever schematic the cluster calls for
# afterwards. That costs one install and reboot after the machine joins, and it cannot supply an
# extension the machine needs in order to boot or to reach Omni in the first place: for those, build
# media with `omni_installation_media_preset` instead.
resource "aws_instance" "worker" {
  ami           = data.aws_ami.talos.id
  instance_type = "m5.large"

  user_data = data.omni_machine_join_config.fleet.config
}

# --- PXE: the kernel arguments on the command line ------------------------------------------------
#
# `kernel_args_cmdline` is the same values already joined with spaces, which is what an iPXE script
# or a hypervisor's boot arguments field wants.
resource "local_file" "ipxe" {
  filename = "${path.module}/boot.ipxe"

  content = <<-SCRIPT
    #!ipxe
    kernel vmlinuz ${data.omni_machine_join_config.fleet.kernel_args_cmdline}
    initrd initramfs.xz
    boot
  SCRIPT
}

# --- Proxmox: the config as a cloud-init user data snippet ----------------------------------------
resource "proxmox_virtual_environment_file" "join_config" {
  content_type = "snippets"
  datastore_id = "local"
  node_name    = "pve"

  source_raw {
    file_name = "talos-join.yaml"
    data      = data.omni_machine_join_config.fleet.config
  }
}

# Everything this data source returns embeds the join token, so every output is sensitive. A plain
# `output` of any of it would be refused unless marked as such, and writing it to a file or a log
# hands the ability to join this instance to whoever can read it.
output "join_kernel_args" {
  value     = data.omni_machine_join_config.fleet.kernel_args_cmdline
  sensitive = true
}

# Omni's machine API URL with the token attached, for the places that want the join URL on its own
# rather than a whole command line.
output "omni_endpoint" {
  value     = data.omni_machine_join_config.fleet.omni_endpoint
  sensitive = true
}
