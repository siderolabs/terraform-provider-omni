data "omni_user" "alice" {
  email = "alice@example.com"
}

output "alice_role" {
  value = data.omni_user.alice.role
}
