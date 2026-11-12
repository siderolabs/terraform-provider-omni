resource "omni_join_token" "fleet" {
  name = "fleet"
}

# Make it the instance default: installation media and machine join configurations that name no
# token of their own will use it from here on. Machines that already joined are unaffected.
resource "omni_default_join_token" "this" {
  token_id = omni_join_token.fleet.id
}

# A join configuration with no token selector follows the default, so it picks up the token above.
data "omni_machine_join_config" "default" {
  depends_on = [omni_default_join_token.this]
}
