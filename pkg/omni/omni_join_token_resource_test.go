// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/siderolabs/omni/client/pkg/omni/resources/siderolink"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

func TestAccOmniJoinTokenResource(t *testing.T) {
	name := randomJoinTokenName("tfa-jt")

	first := joinTokenExpiresIn(720 * time.Hour)
	second := joinTokenExpiresIn(1440 * time.Hour)

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckJoinTokenDestroy,
		Steps: []resource.TestStep{
			{ // create with an expiry
				Config: testAccJoinTokenConfig(name, fmt.Sprintf("  expiration = %q\n", first)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_join_token.test", "name", name),
					// The instant asked for is the instant stored, to the second, rather than
					// whatever a lifetime happened to work out to at apply time.
					resource.TestCheckResourceAttr("omni_join_token.test", "expiration", first),
					resource.TestCheckResourceAttr("omni_join_token.test", "revoked", "false"),
					resource.TestCheckResourceAttr("omni_join_token.test", "state", "Active"),
					resource.TestCheckResourceAttr("omni_join_token.test", "use_count", "0"),
					resource.TestCheckResourceAttr("omni_join_token.test", "is_default", "false"),
					resource.TestCheckResourceAttrSet("omni_join_token.test", "id"),
					testAccCheckJoinTokenExpiration(name, first),
				),
			},
			{ // re-applying the same configuration is a no-op, which a lifetime could never be
				Config:   testAccJoinTokenConfig(name, fmt.Sprintf("  expiration = %q\n", first)),
				PlanOnly: true,
			},
			{ // a token is imported by its value, which is also its ID
				ResourceName:      "omni_join_token.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{ // a changed expiration renews in place rather than replacing
				Config: testAccJoinTokenConfig(name, fmt.Sprintf("  expiration = %q\n", second)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_join_token.test", "expiration", second),
					resource.TestCheckResourceAttr("omni_join_token.test", "state", "Active"),
					testAccCheckJoinTokenExpiration(name, second),
				),
			},
			{ // revoke without destroying
				Config: testAccJoinTokenConfig(name, fmt.Sprintf("  expiration = %q\n  revoked    = true\n", second)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_join_token.test", "revoked", "true"),
					resource.TestCheckResourceAttr("omni_join_token.test", "state", "Revoked"),
					// Revoking must not disturb the expiry.
					resource.TestCheckResourceAttr("omni_join_token.test", "expiration", second),
					testAccCheckJoinToken(name, func(spec *specs.JoinTokenSpec) error {
						if !spec.GetRevoked() {
							return fmt.Errorf("the token was not revoked")
						}

						return nil
					}),
				),
			},
			{ // and back again
				Config: testAccJoinTokenConfig(name, fmt.Sprintf("  expiration = %q\n  revoked    = false\n", second)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_join_token.test", "revoked", "false"),
					resource.TestCheckResourceAttr("omni_join_token.test", "state", "Active"),
				),
			},
			{ // dropping the expiration stops the token expiring, rather than leaving the old instant
				Config: testAccJoinTokenConfig(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("omni_join_token.test", "expiration"),
					testAccCheckJoinToken(name, func(spec *specs.JoinTokenSpec) error {
						if spec.GetExpirationTime() != nil {
							return fmt.Errorf("the expiration was not cleared: %v", spec.GetExpirationTime().AsTime())
						}

						return nil
					}),
				),
			},
		},
	})
}

// TestAccOmniJoinTokenResourceEquivalentSpelling asserts that writing the same instant with an
// offset instead of a Z is not a perpetual diff: the configuration's spelling is kept.
func TestAccOmniJoinTokenResourceEquivalentSpelling(t *testing.T) {
	name := randomJoinTokenName("tfa-jte")

	// The same moment, in a zone that is not UTC. Omni stores and reports UTC.
	instant := time.Now().Add(720 * time.Hour).UTC().Truncate(time.Second)
	offset := instant.In(time.FixedZone("plus-two", 2*60*60)).Format(time.RFC3339)

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckJoinTokenDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccJoinTokenConfig(name, fmt.Sprintf("  expiration = %q\n", offset)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_join_token.test", "expiration", offset),
					testAccCheckJoinTokenExpiration(name, instant.Format(time.RFC3339)),
				),
			},
			{
				Config:   testAccJoinTokenConfig(name, fmt.Sprintf("  expiration = %q\n", offset)),
				PlanOnly: true,
			},
		},
	})
}

// TestAccOmniJoinTokenResourceNoExpiration covers a token that never expires, which is the shape a
// long-lived provisioning token takes.
func TestAccOmniJoinTokenResourceNoExpiration(t *testing.T) {
	name := randomJoinTokenName("tfa-jtn")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckJoinTokenDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccJoinTokenConfig(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_join_token.test", "state", "Active"),
					resource.TestCheckNoResourceAttr("omni_join_token.test", "expiration"),
					testAccCheckJoinToken(name, func(spec *specs.JoinTokenSpec) error {
						if spec.GetExpirationTime() != nil {
							return fmt.Errorf("the token expires despite no expiration: %v", spec.GetExpirationTime().AsTime())
						}

						return nil
					}),
				),
			},
		},
	})
}

// TestAccOmniJoinTokenResourceNameForcesReplacement asserts that renaming replaces the token: Omni
// fixes the name when the token is minted and offers no way to change it.
func TestAccOmniJoinTokenResourceNameForcesReplacement(t *testing.T) {
	first := randomJoinTokenName("tfa-jtr")
	second := randomJoinTokenName("tfa-jt2")

	var firstID string

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckJoinTokenDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccJoinTokenConfig(first, ""),
				Check:  testAccRecordJoinTokenAttr("id", &firstID),
			},
			{
				Config: testAccJoinTokenConfig(second, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_join_token.test", "name", second),
					testAccCheckJoinTokenAttrChanged("id", &firstID),
				),
			},
		},
	})
}

// TestAccOmniJoinTokenResourceRejectsInvalidExpiration asserts a malformed timestamp is refused at
// plan time, and one already gone by at apply time, rather than reaching Omni as a token that is
// dead on arrival or, worse, one that silently never expires.
func TestAccOmniJoinTokenResourceRejectsInvalidExpiration(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{ // a duration is the mistake this attribute invites, so it must fail clearly
				Config:      testAccJoinTokenConfig("tfa-jt-bad", "  expiration = \"720h\"\n"),
				ExpectError: regexp.MustCompile(`not\s+an\s+RFC\s+3339\s+timestamp`),
			},
			{
				Config:      testAccJoinTokenConfig("tfa-jt-bad", "  expiration = \"2027-01-01\"\n"),
				ExpectError: regexp.MustCompile(`not\s+an\s+RFC\s+3339\s+timestamp`),
			},
			{
				Config:      testAccJoinTokenConfig("tfa-jt-bad", "  expiration = \"2000-01-01T00:00:00Z\"\n"),
				ExpectError: regexp.MustCompile(`is\s+in\s+the\s+past`),
			},
		},
	})
}

// TestAccOmniJoinTokenResourceRejectsLongName asserts Omni's 16-character limit is enforced at plan
// time rather than reaching Omni as an InvalidArgument.
func TestAccOmniJoinTokenResourceRejectsLongName(t *testing.T) {
	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccJoinTokenConfig("tf-acc-join-token-name", ""),
				ExpectError: regexp.MustCompile(`Invalid\s+Attribute\s+Value\s+Length`),
			},
		},
	})
}

// randomJoinTokenName builds a unique name inside Omni's 16-character limit on a join token name,
// which acctest.RandomWithPrefix overshoots on its own.
func randomJoinTokenName(prefix string) string {
	return prefix + "-" + acctest.RandStringFromCharSet(6, acctest.CharSetAlphaNum)
}

func testAccJoinTokenConfig(name, extra string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_join_token" "test" {
  name = %q
%s}
`, name, extra)
}

// joinTokenExpiresIn renders an expiration the given distance from now, in the spelling Omni
// reports back, so a test can assert the exact attribute value.
func joinTokenExpiresIn(d time.Duration) string {
	return time.Now().Add(d).UTC().Truncate(time.Second).Format(time.RFC3339)
}

// testAccCheckJoinTokenExpiration asserts, via the live Omni API, that the stored instant is the one
// the configuration asked for, which is what a lifetime-based attribute could never guarantee.
func testAccCheckJoinTokenExpiration(name, want string) resource.TestCheckFunc {
	return testAccCheckJoinToken(name, func(spec *specs.JoinTokenSpec) error {
		stored := spec.GetExpirationTime()
		if stored == nil {
			return fmt.Errorf("the token has no expiration, want %s", want)
		}

		if got := stored.AsTime().UTC().Format(time.RFC3339); got != want {
			return fmt.Errorf("stored expiration is %s, want %s", got, want)
		}

		return nil
	})
}

// testAccRecordJoinTokenAttr stores an attribute value so a later step can compare against it.
func testAccRecordJoinTokenAttr(attr string, into *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		value, err := joinTokenAttr(s, attr)
		if err != nil {
			return err
		}

		*into = value

		return nil
	}
}

// testAccCheckJoinTokenAttrChanged asserts an attribute differs from a previously recorded value.
func testAccCheckJoinTokenAttrChanged(attr string, previous *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		value, err := joinTokenAttr(s, attr)
		if err != nil {
			return err
		}

		if value == *previous {
			return fmt.Errorf("attribute %q did not change, still %q", attr, value)
		}

		return nil
	}
}

// joinTokenAttr reads a non-empty attribute off the test's join token resource.
func joinTokenAttr(s *terraform.State, attr string) (string, error) {
	rs, ok := s.RootModule().Resources["omni_join_token.test"]
	if !ok {
		return "", fmt.Errorf("resource omni_join_token.test not found in state")
	}

	value := rs.Primary.Attributes[attr]
	if value == "" {
		return "", fmt.Errorf("attribute %q of omni_join_token.test is empty", attr)
	}

	return value, nil
}

// testAccCheckJoinToken reads the token back over the live Omni API, looking it up by name so the
// assertions never need the secret, and hands its spec to the caller.
func testAccCheckJoinToken(name string, check func(*specs.JoinTokenSpec) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		client, err := newTestClient()
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck

		tokens, err := safe.ReaderListAll[*siderolink.JoinToken](context.Background(), client.Omni().State())
		if err != nil {
			return fmt.Errorf("failed to list join tokens: %w", err)
		}

		for token := range tokens.All() {
			if token.TypedSpec().Value.GetName() == name {
				return check(token.TypedSpec().Value)
			}
		}

		return fmt.Errorf("join token named %q does not exist", name)
	}
}

// testAccCheckJoinTokenDestroy asserts, via the live Omni API, that every token the test managed is
// gone.
func testAccCheckJoinTokenDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "omni_join_token" {
			continue
		}

		client, err := newTestClient()
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck

		id := rs.Primary.Attributes["id"]

		_, err = safe.ReaderGetByID[*siderolink.JoinToken](context.Background(), client.Omni().State(), id)
		if err == nil {
			return fmt.Errorf("join token %q still exists", rs.Primary.Attributes["name"])
		}

		if !cosistate.IsNotFoundError(err) {
			return fmt.Errorf("unexpected error checking join token %q: %w", rs.Primary.Attributes["name"], err)
		}
	}

	return nil
}
