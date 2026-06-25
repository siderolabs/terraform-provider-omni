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
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	omniclient "github.com/siderolabs/omni/client/pkg/client"
	"github.com/siderolabs/omni/client/pkg/omni/resources/auth"
)

func TestAccOmniUserResource(t *testing.T) {
	email := fmt.Sprintf("%s@example.com", acctest.RandomWithPrefix("tf-acc"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckUserDestroy,
		Steps: []resource.TestStep{
			{ // create
				Config: testAccUserConfig(email, "Reader"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_user.test", "email", email),
					resource.TestCheckResourceAttr("omni_user.test", "role", "Reader"),
					resource.TestCheckResourceAttrSet("omni_user.test", "id"),
					testAccCheckUserRole(email, "Reader"),
				),
			},
			{ // import by email
				ResourceName:      "omni_user.test",
				ImportState:       true,
				ImportStateId:     email,
				ImportStateVerify: true,
			},
			{ // update the role in place
				Config: testAccUserConfig(email, "Operator"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_user.test", "role", "Operator"),
					testAccCheckUserRole(email, "Operator"),
				),
			},
		},
	})
}

func testAccUserConfig(email, role string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_user" "test" {
  email = %q
  role  = %q
}
`, email, role)
}

// newTestClient builds an Omni client from the acceptance-test environment.
func newTestClient() (*omniclient.Client, error) {
	return omniclient.New(
		os.Getenv("OMNI_ENDPOINT"),
		omniclient.WithServiceAccount(os.Getenv("OMNI_SERVICE_ACCOUNT_KEY")),
		omniclient.WithInsecureSkipTLSVerify(true),
	)
}

// testAccCheckUserRole asserts, via the live Omni API, that the identity exists and its user has
// the expected role.
func testAccCheckUserRole(email, role string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		client, err := newTestClient()
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck

		st := client.Omni().State()

		identity, err := safe.ReaderGetByID[*auth.Identity](context.Background(), st, email)
		if err != nil {
			return fmt.Errorf("failed to read identity %q: %w", email, err)
		}

		user, err := safe.ReaderGetByID[*auth.User](context.Background(), st, identity.TypedSpec().Value.GetUserId())
		if err != nil {
			return fmt.Errorf("failed to read user for %q: %w", email, err)
		}

		if got := user.TypedSpec().Value.GetRole(); got != role {
			return fmt.Errorf("unexpected role for %q: got %q, want %q", email, got, role)
		}

		return nil
	}
}

// testAccCheckUserDestroy asserts, via the live Omni API, that every managed identity is gone.
func testAccCheckUserDestroy(s *terraform.State) error {
	client, err := newTestClient()
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck

	st := client.Omni().State()

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "omni_user" {
			continue
		}

		email := rs.Primary.Attributes["email"]

		_, err := safe.ReaderGetByID[*auth.Identity](context.Background(), st, email)
		if err == nil {
			return fmt.Errorf("omni identity %q still exists", email)
		}

		if !cosistate.IsNotFoundError(err) {
			return fmt.Errorf("unexpected error checking identity %q: %w", email, err)
		}
	}

	return nil
}
