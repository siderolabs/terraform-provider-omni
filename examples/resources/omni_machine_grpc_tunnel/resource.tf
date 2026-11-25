# Tunnel a machine's SideroLink traffic through gRPC, for a machine behind a network that blocks
# or mangles the WireGuard UDP traffic. Destroying the resource hands the choice back to the machine.
resource "omni_machine_grpc_tunnel" "edge_0" {
  machine_id = omni_machine_wait.edge_0.machine_id
  enabled    = true
}
