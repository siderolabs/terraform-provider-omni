# Omni names a machine by its UUID, which is not known until the machine has joined. Put a label
# unique to the machine into its join config: Omni signs it into the join token and copies it onto the
# machine when it registers, which is how to find the machine afterwards.
data "omni_machine_join_config" "worker_0" {
  machine_labels = {
    "terraform/machine" = "worker-0"
  }
}

resource "aws_instance" "worker_0" {
  ami           = data.aws_ami.talos.id
  instance_type = "m5.large"
  user_data     = data.omni_machine_join_config.worker_0.config
}

# Waits for the machine carrying the label to register and connect, and records its UUID.
resource "omni_machine_wait" "worker_0" {
  labels = {
    "terraform/machine" = "worker-0"
  }

  # A replaced instance is a new machine: wait for it again rather than keep the old UUID.
  triggers = {
    instance_id = aws_instance.worker_0.id
  }

  # The instance is destroyed together with this resource, so drop the machine from Omni as well.
  remove_on_destroy = true

  timeouts = {
    create = "20m"
  }
}

resource "omni_machine_set_node" "worker_0" {
  cluster     = omni_cluster.example.name
  machine_set = omni_machine_set.workers.id
  machine_id  = omni_machine_wait.worker_0.machine_id
}

# Where the UUID is known in advance, for instance because it was set as the VM's SMBIOS UUID, wait
# for it directly.
resource "omni_machine_wait" "known" {
  machine_id = "4c4c4544-0042-3510-8051-b4c04f4e4d32"
}
