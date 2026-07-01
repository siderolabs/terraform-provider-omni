// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/go-jose/go-jose/v4/testutils/require"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	omnires "github.com/siderolabs/omni/client/pkg/omni/resources/omni"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

const (
	talosVersion      = "1.13.5"
	kubernetesVersion = "1.36.2"
)

func TestAccOmniClusterResource(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc")

	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { waitForVersionsReady(t, talosVersion, kubernetesVersion) },
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckClusterDestroy,
		Steps: []resource.TestStep{
			{ // create
				Config: testAccClusterConfig(name, talosVersion, kubernetesVersion, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_cluster.test", "name", name),
					resource.TestCheckResourceAttr("omni_cluster.test", "kubernetes_version", kubernetesVersion),
					resource.TestCheckResourceAttr("omni_cluster.test", "talos_version", talosVersion),
					resource.TestCheckResourceAttr("omni_cluster.test", "features.enable_workload_proxy", "true"),
					testAccCheckClusterVersions(name, kubernetesVersion, talosVersion),
				),
			},
			{ // import by name
				ResourceName:                         "omni_cluster.test",
				ImportState:                          true,
				ImportStateId:                        name,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "name",
			},
			{ // toggle a feature in place
				Config: testAccClusterConfig(name, talosVersion, kubernetesVersion, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_cluster.test", "features.enable_workload_proxy", "false"),
				),
			},
		},
	})
}

func testAccClusterConfig(name, talosVersion, kubernetesVersion string, workloadProxy bool) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_cluster" "test" {
  name               = %q
  kubernetes_version = %q
  talos_version      = %q

  features = {
    enable_workload_proxy = %t
  }
}
`, name, kubernetesVersion, talosVersion, workloadProxy)
}

// testAccCheckClusterVersions asserts, via the live Omni API, that the cluster exists with the
// expected versions.
func testAccCheckClusterVersions(name, kubernetesVersion, talosVersion string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		client, err := newTestClient()
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck

		cluster, err := safe.ReaderGetByID[*omnires.Cluster](context.Background(), client.Omni().State(), name)
		if err != nil {
			return fmt.Errorf("failed to read cluster %q: %w", name, err)
		}

		if got := cluster.TypedSpec().Value.GetKubernetesVersion(); got != kubernetesVersion {
			return fmt.Errorf("unexpected Kubernetes version for %q: got %q, want %q", name, got, kubernetesVersion)
		}

		if got := cluster.TypedSpec().Value.GetTalosVersion(); got != talosVersion {
			return fmt.Errorf("unexpected Talos version for %q: got %q, want %q", name, got, talosVersion)
		}

		return nil
	}
}

// testAccCheckClusterDestroy asserts, via the live Omni API, that every managed cluster is gone.
func testAccCheckClusterDestroy(s *terraform.State) error {
	client, err := newTestClient()
	if err != nil {
		return err
	}
	defer client.Close() //nolint:errcheck

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "omni_cluster" {
			continue
		}

		_, err := safe.ReaderGetByID[*omnires.Cluster](context.Background(), client.Omni().State(), rs.Primary.Attributes["name"])
		if err == nil {
			return fmt.Errorf("omni cluster %q still exists", rs.Primary.Attributes["name"])
		}

		if !cosistate.IsNotFoundError(err) {
			return fmt.Errorf("unexpected error checking cluster %q: %w", rs.Primary.Attributes["name"], err)
		}
	}

	return nil
}

func waitForVersionsReady(t *testing.T, expectedTalosVersion, expectedKubernetesVersion string) {
	t.Helper()

	client, err := newTestClient()
	require.NoError(t, err)

	defer client.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err = client.Omni().State().WatchFor(ctx, omnires.NewTalosVersion(expectedTalosVersion).Metadata(), cosistate.WithEventTypes(cosistate.Created, cosistate.Updated))
	require.NoError(t, err)

	_, err = client.Omni().State().WatchFor(ctx, omnires.NewKubernetesVersion(expectedKubernetesVersion).Metadata(), cosistate.WithEventTypes(cosistate.Created, cosistate.Updated))
	require.NoError(t, err)
}
