// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

func TestAccOmniMachineJoinConfigDataSource(t *testing.T) {
	name := randomJoinTokenName("tfa-jc")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckJoinTokenDestroy,
		Steps: []resource.TestStep{
			{ // selected by token value
				Config: testAccMachineJoinConfigConfig(name, "  token_id = omni_join_token.test.id\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(
						"data.omni_machine_join_config.test", "join_token",
						"omni_join_token.test", "id",
					),
					// The config is a Talos machine configuration fragment carrying the SideroLink
					// API URL, which is what actually makes the machine register.
					resource.TestMatchResourceAttr("data.omni_machine_join_config.test", "config",
						regexp.MustCompile(`(?s)apiVersion:\s*v1alpha1.*kind:\s*SideroLinkConfig.*apiUrl:`)),
					testAccCheckJoinConfigCarriesToken("config"),
					resource.TestCheckResourceAttrSet("data.omni_machine_join_config.test", "kernel_args.0"),
					testAccCheckJoinConfigCarriesToken("kernel_args_cmdline"),
					resource.TestMatchResourceAttr("data.omni_machine_join_config.test", "kernel_args_cmdline",
						regexp.MustCompile(`siderolink\.api=`)),
					testAccCheckJoinConfigCarriesToken("omni_endpoint"),
					// The scheme follows how the instance advertises its machine API, which is
					// https on a public instance and grpc where SideroLink is reached directly.
					resource.TestMatchResourceAttr("data.omni_machine_join_config.test", "omni_endpoint",
						regexp.MustCompile(`^[a-z]+://.+\?jointoken=`)),
				),
			},
			{ // selected by name, which keeps the secret out of the configuration
				Config: testAccMachineJoinConfigConfig(name, fmt.Sprintf(`  token_name = %q

  depends_on = [omni_join_token.test]
`, name)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(
						"data.omni_machine_join_config.test", "join_token",
						"omni_join_token.test", "id",
					),
					testAccCheckJoinConfigCarriesToken("config"),
				),
			},
			{ // the gRPC tunnel variant, for machines that cannot reach the SideroLink UDP port
				Config: testAccMachineJoinConfigConfig(name, "  token_id        = omni_join_token.test.id\n  use_grpc_tunnel = true\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr("data.omni_machine_join_config.test", "config",
						regexp.MustCompile(`(?i)tunnel`)),
					// The standalone endpoint carries the tunnel setting too, so it works on its own.
					resource.TestMatchResourceAttr("data.omni_machine_join_config.test", "omni_endpoint",
						regexp.MustCompile(`(?i)grpc_tunnel=true`)),
				),
			},
		},
	})
}

// TestAccOmniMachineJoinConfigDataSourceDefaultToken covers the no-selection path, which follows the
// instance default.
//
// Not a parallel test: it reads instance-wide state that TestAccOmniDefaultJoinTokenResource moves.
func TestAccOmniMachineJoinConfigDataSourceDefaultToken(t *testing.T) {
	skipWithoutLiveOmni(t)

	expected, err := currentDefaultJoinToken()
	if err != nil {
		t.Fatalf("failed to read the current default join token: %v", err)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "omni" {
  insecure_skip_tls_verify = true
}

data "omni_machine_join_config" "test" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.omni_machine_join_config.test", "join_token", expected),
					resource.TestCheckResourceAttrSet("data.omni_machine_join_config.test", "config"),
					resource.TestCheckResourceAttrSet("data.omni_machine_join_config.test", "kernel_args.0"),
				),
			},
		},
	})
}

// TestAccOmniMachineJoinConfigDataSourceRejectsBothSelectors asserts the two token selectors are
// refused together at plan time rather than one silently winning.
func TestAccOmniMachineJoinConfigDataSourceRejectsBothSelectors(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "omni" {
  insecure_skip_tls_verify = true
}

data "omni_machine_join_config" "test" {
  token_id   = "whatever"
  token_name = "whatever"
}
`,
				ExpectError: regexp.MustCompile(`Invalid\s+Attribute\s+Combination`),
			},
		},
	})
}

// TestAccOmniMachineJoinConfigDataSourceUnknownToken asserts a name that matches nothing is reported
// as such rather than producing a configuration against the wrong token.
func TestAccOmniMachineJoinConfigDataSourceUnknownToken(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "omni" {
  insecure_skip_tls_verify = true
}

data "omni_machine_join_config" "test" {
  token_name = "tfa-jc-none"
}
`,
				ExpectError: regexp.MustCompile(`No\s+join\s+token\s+named`),
			},
		},
	})
}

func testAccMachineJoinConfigConfig(name, selector string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_join_token" "test" {
  name = %q
}

data "omni_machine_join_config" "test" {
%s}
`, name, selector)
}

// testAccCheckJoinConfigCarriesToken asserts the given attribute embeds the token the test created,
// which is the whole point of the data source.
func testAccCheckJoinConfigCarriesToken(attr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		token, ok := s.RootModule().Resources["omni_join_token.test"]
		if !ok {
			return fmt.Errorf("resource omni_join_token.test not found in state")
		}

		joinConfig, ok := s.RootModule().Resources["data.omni_machine_join_config.test"]
		if !ok {
			return fmt.Errorf("data source omni_machine_join_config.test not found in state")
		}

		value := joinConfig.Primary.Attributes[attr]
		if value == "" {
			return fmt.Errorf("attribute %q is empty", attr)
		}

		if !strings.Contains(value, token.Primary.Attributes["id"]) {
			return fmt.Errorf("attribute %q does not carry the join token", attr)
		}

		return nil
	}
}
