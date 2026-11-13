// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// TestAccOmniDefaultJoinTokenResource points the instance default at a token the test creates and
// then puts it back.
//
// Not a parallel test: it changes instance-wide state that every machine joining without an explicit
// token depends on, and the final step restores the token the instance started with so nothing is
// left pointing at a token this test destroys.
func TestAccOmniDefaultJoinTokenResource(t *testing.T) {
	skipWithoutLiveOmni(t)

	name := randomJoinTokenName("tfa-jtd")

	original, err := currentDefaultJoinToken()
	if err != nil {
		t.Fatalf("failed to read the current default join token: %v", err)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckJoinTokenDestroy,
		Steps: []resource.TestStep{
			{ // adopt the existing setting and point it at the new token
				Config: testAccDefaultJoinTokenConfig(name, "omni_join_token.test.id"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_default_join_token.test", "id", "default"),
					resource.TestCheckResourceAttrPair(
						"omni_default_join_token.test", "token_id",
						"omni_join_token.test", "id",
					),
					// Asserted against the live instance rather than against the token resource's
					// own is_default: that was read when the token was created, which is before
					// this resource pointed the default at it, and Terraform does not refresh it
					// within a single apply.
					testAccCheckDefaultJoinTokenMatches("omni_join_token.test"),
				),
			},
			{ // importing reads whatever the instance currently has, whatever ID is given
				ResourceName:      "omni_default_join_token.test",
				ImportState:       true,
				ImportStateId:     "default",
				ImportStateVerify: true,
			},
			{ // hand the default back before the token is destroyed
				Config: testAccDefaultJoinTokenConfig(name, fmt.Sprintf("%q", original)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_default_join_token.test", "token_id", original),
				),
			},
		},
	})
}

func testAccDefaultJoinTokenConfig(name, tokenRef string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_join_token" "test" {
  name = %q
}

resource "omni_default_join_token" "test" {
  token_id = %s
}
`, name, tokenRef)
}

// skipWithoutLiveOmni skips a test that reads the Omni instance while building its configuration,
// which happens before the acceptance framework applies its own TF_ACC gate.
func skipWithoutLiveOmni(t *testing.T) {
	t.Helper()

	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests are skipped unless TF_ACC is set")
	}
}

// currentDefaultJoinToken reads the instance default over the live Omni API, so the test can put it
// back afterwards.
func currentDefaultJoinToken() (string, error) {
	client, err := newTestClient()
	if err != nil {
		return "", err
	}
	defer client.Close() //nolint:errcheck

	defaultToken, err := safe.ReaderGetByID[*siderolink.DefaultJoinToken](
		context.Background(), client.Omni().State(), siderolink.DefaultJoinTokenID,
	)
	if err != nil {
		return "", err
	}

	return defaultToken.TypedSpec().Value.GetTokenId(), nil
}

// testAccCheckDefaultJoinTokenMatches asserts, via the live Omni API, that the instance default is
// the token the given resource manages.
func testAccCheckDefaultJoinTokenMatches(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %q not found in state", resourceName)
		}

		current, err := currentDefaultJoinToken()
		if err != nil {
			return fmt.Errorf("failed to read the default join token: %w", err)
		}

		if current != rs.Primary.Attributes["id"] {
			return fmt.Errorf("the default join token is not the one managed by %q", resourceName)
		}

		return nil
	}
}
