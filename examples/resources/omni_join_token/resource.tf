# A long-lived token for a provisioning pipeline that mints machines continuously.
resource "omni_join_token" "provisioning" {
  name = "provisioning"
}

# A short-lived one for a single batch of machines. Once the batch has joined, letting the token
# expire stops anything else using it, while the machines that already joined stay connected.
#
# The expiration is an absolute instant, not a lifetime, so this configuration describes the same
# expiry no matter how long after it was written it is applied, and re-applying it changes nothing.
resource "omni_join_token" "batch_2026_q1" {
  name       = "batch-2026-q1"
  expiration = "2026-04-01T00:00:00Z"
}

# An expiry that rolls forward on its own. `time_rotating` moves only when it is due, so the
# expiration below is stable between rotations: every 30 days it jumps to 90 days out, renewing the
# token in place without replacing it or changing its value.
#
# Do not use `timestamp()` here. It changes on every plan, so the token would be renewed on every
# apply and the resource would never be clean.
resource "time_rotating" "fleet_token" {
  rotation_days = 30
}

resource "omni_join_token" "fleet" {
  name = "fleet"

  # 90 days past each rotation, leaving a 60-day overlap in which to re-render whatever boot media
  # or iPXE script carries the token.
  expiration = timeadd(time_rotating.fleet_token.rfc3339, "2160h")
}

# Shutting a token off without destroying it. Revoking stops new machines joining and is reversible;
# destroying is not, and `use_count` says how many machines would lose the ability to re-register.
# Prefer this to backdating `expiration`, which the provider refuses.
resource "omni_join_token" "retired" {
  name    = "retired"
  revoked = true
}

# The token value is the secret a machine presents. Anything it is written into, a kernel command
# line or a boot script, hands the ability to join this instance to whoever can read it.
output "provisioning_token" {
  value     = omni_join_token.provisioning.id
  sensitive = true
}

# `expiration` is the signal to renew before machines stop being able to join.
output "batch_token_expires_at" {
  value = omni_join_token.batch_2026_q1.expiration
}

# Baking a token into installation media, so a machine booted from it joins with that token rather
# than with the instance default.
resource "omni_installation_media_preset" "bare_metal" {
  name       = "bare-metal"
  join_token = omni_join_token.provisioning.id

  machine_labels = {
    source = "provisioning"
  }
}
