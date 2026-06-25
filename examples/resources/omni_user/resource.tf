resource "omni_user" "alice" {
  email = "alice@example.com"
  role  = "Operator"
}

output "alice_id" {
  value = omni_user.alice.id
}
