# The labels are imported by the machine UUID. The import adopts every label on the machine that
# the configuration can hold, leaving those reserved by Omni alone, so list them all in the
# configuration: an adopted label missing from it is removed on the next apply.
terraform import omni_machine_labels.worker_0 e8b8e0f0-1111-2222-3333-444455556666
