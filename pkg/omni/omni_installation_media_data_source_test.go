// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// TestAccOmniInstallationMediaDataSource resolves a bare-metal preset into a URL.
//
// It needs egress to the image factory: reading the data source registers the preset's schematic
// there. The preset deliberately leaves the Talos version unpinned, so the test does not depend on
// Omni having synced its version catalog, which it does asynchronously on startup.
func TestAccOmniInstallationMediaDataSource(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-media")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{ // an ISO, the default format
				Config: testAccInstallationMediaDataSourceConfig(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.omni_installation_media.test", "schematic_id"),
					resource.TestCheckResourceAttrSet("data.omni_installation_media.test", "storage_key"),
					// The preset leaves the version unpinned, so Omni resolves its own default and the
					// data source has no concrete version to report.
					resource.TestCheckNoResourceAttr("data.omni_installation_media.test", "talos_version"),
					resource.TestCheckResourceAttr("data.omni_installation_media.test", "filename", "metal-amd64.iso"),
					resource.TestCheckResourceAttr("data.omni_installation_media.test", "platform", "metal"),
					resource.TestCheckResourceAttr("data.omni_installation_media.test", "architecture", "amd64"),
					testAccCheckMediaURL(".iso"),
				),
			},
			{ // an explicit format reaches the factory as a disk image
				Config: testAccInstallationMediaDataSourceConfig(name, `  format = "raw"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.omni_installation_media.test", "filename", "metal-amd64.raw.xz"),
					testAccCheckMediaURL(".raw.xz"),
				),
			},
			{ // an explicit suffix is passed to the factory as given
				Config: testAccInstallationMediaDataSourceConfig(name, `  format = "raw.zst"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.omni_installation_media.test", "filename", "metal-amd64.raw.zst"),
					resource.TestCheckResourceAttr("data.omni_installation_media.test", "platform", "metal"),
					resource.TestCheckResourceAttr("data.omni_installation_media.test", "architecture", "amd64"),
					testAccCheckMediaURL(".raw.zst"),
				),
			},
			{ // PXE yields a script URL with no file extension
				Config: testAccInstallationMediaDataSourceConfig(name, `  format = "pxe"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.omni_installation_media.test", "filename", "metal-amd64"),
					testAccCheckMediaURL(""),
				),
			},
		},
	})
}

// TestAccOmniInstallationMediaDataSourceSchematicStable asserts that resolving the same preset twice
// yields the same schematic and medium: the factory deduplicates schematics by content, so a repeated
// read must not churn.
func TestAccOmniInstallationMediaDataSourceSchematicStable(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-media-stable")

	var schematicID, storageKey string

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccInstallationMediaDataSourceConfig(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccRecordServiceAccountAttr("data.omni_installation_media.test", "schematic_id", &schematicID),
					testAccRecordServiceAccountAttr("data.omni_installation_media.test", "storage_key", &storageKey),
				),
			},
			{
				Config: testAccInstallationMediaDataSourceConfig(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckAttrUnchanged("data.omni_installation_media.test", "schematic_id", &schematicID),
					testAccCheckAttrUnchanged("data.omni_installation_media.test", "storage_key", &storageKey),
				),
			},
		},
	})
}

// TestAccOmniInstallationMediaDataSourceRejectsFormat asserts that a format is refused for a preset
// whose format is implied, matching `omnictl media download`.
func TestAccOmniInstallationMediaDataSourceRejectsFormat(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-media-cloud")

	resource.ParallelTest(t, resource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_installation_media_preset" "test" {
  name         = %q
  architecture = "amd64"

  cloud = {
    platform = "aws"
  }
}

data "omni_installation_media" "test" {
  preset = omni_installation_media_preset.test.name
  format = "iso"
}
`, name),
				ExpectError: regexp.MustCompile(`format\s+is\s+not\s+applicable\s+to\s+the\s+cloud\s+preset`),
			},
		},
	})
}

func testAccInstallationMediaDataSourceConfig(name, extra string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_installation_media_preset" "test" {
  name         = %q
  architecture = "amd64"
}

data "omni_installation_media" "test" {
  preset = omni_installation_media_preset.test.name
%s
}
`, name, extra)
}

// testAccCheckMediaURL asserts the resolved URL is absolute, carries the schematic ID, and ends in
// the expected suffix before any query string.
func testAccCheckMediaURL(suffix string) resource.TestCheckFunc {
	const resourceName = "data.omni_installation_media.test"

	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("data source %q not found in state", resourceName)
		}

		raw := rs.Primary.Attributes["url"]
		if raw == "" {
			return fmt.Errorf("%q has an empty url", resourceName)
		}

		parsed, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("url %q does not parse: %w", raw, err)
		}

		if !parsed.IsAbs() {
			return fmt.Errorf("url %q is not absolute", raw)
		}

		schematicID := rs.Primary.Attributes["schematic_id"]
		if !strings.Contains(parsed.Path, schematicID) {
			return fmt.Errorf("url path %q does not contain schematic ID %q", parsed.Path, schematicID)
		}

		if !strings.HasSuffix(parsed.Path, suffix) {
			return fmt.Errorf("url path %q does not end in %q", parsed.Path, suffix)
		}

		return nil
	}
}

// testAccCheckAttrUnchanged asserts an attribute still matches a previously recorded value.
func testAccCheckAttrUnchanged(resourceName, attr string, previous *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %q not found in state", resourceName)
		}

		if got := rs.Primary.Attributes[attr]; got != *previous {
			return fmt.Errorf("attribute %q of %q changed from %q to %q", attr, resourceName, *previous, got)
		}

		return nil
	}
}
