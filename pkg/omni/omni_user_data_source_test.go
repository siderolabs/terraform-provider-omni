// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

func TestAccOmniUserDataSource(t *testing.T) {
	email := fmt.Sprintf("%s@example.com", acctest.RandomWithPrefix("tf-acc"))

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckUserDestroy,
		Steps: []resource.TestStep{
			{ // look up the user created by the resource
				Config: testAccUserDataSourceConfig(email, "Operator"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.omni_user.test", "email", email),
					resource.TestCheckResourceAttr("data.omni_user.test", "role", "Operator"),
					resource.TestCheckResourceAttrSet("data.omni_user.test", "id"),
					// the data source must resolve to the same user the resource created
					resource.TestCheckResourceAttrPair(
						"data.omni_user.test", "id",
						"omni_user.test", "id",
					),
					resource.TestCheckResourceAttrPair(
						"data.omni_user.test", "role",
						"omni_user.test", "role",
					),
				),
			},
		},
	})
}

func testAccUserDataSourceConfig(email, role string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_user" "test" {
  email = %q
  role  = %q
}

data "omni_user" "test" {
  email = omni_user.test.email
}
`, email, role)
}
