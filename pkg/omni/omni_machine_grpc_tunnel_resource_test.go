// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/google/uuid"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// TestAccOmniMachineGRPCTunnelResource exercises the full lifecycle of a machine's gRPC tunnel
// setting: enable, import, switch to UDP in place, and destroy, which hands the choice back to the
// machine.
//
// Omni accepts the setting before the machine has joined, so the test uses a random UUID rather
// than a real machine.
func TestAccOmniMachineGRPCTunnelResource(t *testing.T) {
	machineID := uuid.NewString()

	tfresource.ParallelTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckMachineGRPCTunnelDestroy,
		Steps: []tfresource.TestStep{
			{ // tunnel through gRPC
				Config: testAccMachineGRPCTunnelConfig(machineID, true),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("omni_machine_grpc_tunnel.test", "machine_id", machineID),
					tfresource.TestCheckResourceAttr("omni_machine_grpc_tunnel.test", "enabled", "true"),
					testAccCheckMachineGRPCTunnel(machineID, true),
				),
			},
			{ // import by machine UUID
				ResourceName:                         "omni_machine_grpc_tunnel.test",
				ImportState:                          true,
				ImportStateId:                        machineID,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "machine_id",
			},
			{ // switching to UDP is an in-place update
				Config: testAccMachineGRPCTunnelConfig(machineID, false),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("omni_machine_grpc_tunnel.test", "enabled", "false"),
					testAccCheckMachineGRPCTunnel(machineID, false),
				),
			},
		},
	})
}

func testAccMachineGRPCTunnelConfig(machineID string, enabled bool) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_machine_grpc_tunnel" "test" {
  machine_id = %q
  enabled    = %t
}
`, machineID, enabled)
}

// testAccCheckMachineGRPCTunnel asserts, via the live Omni API, the stored tunnel mode.
func testAccCheckMachineGRPCTunnel(machineID string, enabled bool) tfresource.TestCheckFunc {
	return func(*terraform.State) error {
		client, err := newTestClient()
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck

		config, err := safe.ReaderGetByID[*siderolink.GRPCTunnelConfig](context.Background(), client.Omni().State(), machineID)
		if err != nil {
			return fmt.Errorf("failed to read the gRPC tunnel configuration %q: %w", machineID, err)
		}

		if got := config.TypedSpec().Value.GetEnabled(); got != enabled {
			return fmt.Errorf("unexpected gRPC tunnel mode for %q: got %t, want %t", machineID, got, enabled)
		}

		return nil
	}
}

// testAccCheckMachineGRPCTunnelDestroy asserts, via the live Omni API, that every managed setting
// is gone.
func testAccCheckMachineGRPCTunnelDestroy(s *terraform.State) error {
	client, err := newTestClient()
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "omni_machine_grpc_tunnel" {
			continue
		}

		machineID := rs.Primary.Attributes["machine_id"]

		_, err = safe.ReaderGetByID[*siderolink.GRPCTunnelConfig](context.Background(), client.Omni().State(), machineID)
		if err == nil {
			return fmt.Errorf("gRPC tunnel configuration %q still exists", machineID)
		}

		if !cosistate.IsNotFoundError(err) {
			return fmt.Errorf("failed to check the gRPC tunnel configuration %q: %w", machineID, err)
		}
	}

	return nil
}
