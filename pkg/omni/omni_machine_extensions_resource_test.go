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

	"github.com/cosi-project/runtime/pkg/resource"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	omnires "github.com/siderolabs/omni/client/pkg/omni/resources/omni"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// helloWorldExtension is the example system extension used by the acceptance test. Omni validates
// requested extensions against the Talos extensions catalog, so the test waits for this extension
// to be present before applying.
const helloWorldExtension = "siderolabs/hello-world-service"

// TestAccOmniMachineExtensionsResource exercises cluster- and machine-set-scoped extensions
// configurations, an in-place update of the extensions set, import, and the
// machine_set/cluster_machine mutual-exclusivity guard.
func TestAccOmniMachineExtensionsResource(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc")

	tfresource.ParallelTest(t, tfresource.TestCase{
		PreCheck: func() {
			waitForVersionsReady(t, talosVersion, kubernetesVersion)
			waitForExtensionReady(t, talosVersion, helloWorldExtension)
		},
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckClusterDestroy,
		Steps: []tfresource.TestStep{
			{ // create cluster-scoped and machine-set-scoped extensions
				Config: testAccMachineExtensionsConfig(name, talosVersion, kubernetesVersion, fmt.Sprintf("%q", helloWorldExtension)),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("omni_machine_extensions.cluster", "id", "schematic-"+name),
					tfresource.TestCheckResourceAttr("omni_machine_extensions.cluster", "extensions.#", "1"),
					tfresource.TestCheckResourceAttr("omni_machine_extensions.workers", "id", "schematic-"+name+"-workers"),
					tfresource.TestCheckResourceAttr("omni_machine_extensions.workers", "extensions.#", "1"),
				),
			},
			{ // import the machine-set-scoped extensions by ID
				ResourceName:      "omni_machine_extensions.workers",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{ // clearing the extensions set is an in-place update
				Config: testAccMachineExtensionsConfig(name, talosVersion, kubernetesVersion, ""),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("omni_machine_extensions.workers", "extensions.#", "0"),
				),
			},
		},
	})
}

// TestAccOmniMachineExtensionsConflicting asserts that setting both machine_set and cluster_machine
// is rejected. It stands alone (no CheckDestroy) because the config never plans successfully, so
// nothing is created.
func TestAccOmniMachineExtensionsConflicting(t *testing.T) {
	tfresource.ParallelTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config:      testAccMachineExtensionsConfigConflicting(),
				ExpectError: regexp.MustCompile("Invalid Attribute Combination"),
			},
		},
	})
}

// waitForExtensionReady waits for the Talos extensions catalog for the given version to be synced
// from the image factory and to contain the given extension.
func waitForExtensionReady(t *testing.T, talosVersion, extension string) {
	t.Helper()

	client, err := newTestClient()
	if err != nil {
		t.Fatalf("failed to build Omni client: %v", err)
	}

	defer client.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	_, err = client.Omni().State().WatchFor(ctx,
		omnires.NewTalosExtensions(talosVersion).Metadata(),
		cosistate.WithCondition(func(res resource.Resource) (bool, error) {
			catalog, ok := res.(*omnires.TalosExtensions)
			if !ok {
				return false, nil
			}

			for _, item := range catalog.TypedSpec().Value.GetItems() {
				if item.GetName() == extension {
					return true, nil
				}
			}

			return false, nil
		}),
	)
	if err != nil {
		t.Fatalf("waiting for Talos extension %q (version %q) to become available: %v", extension, talosVersion, err)
	}
}

func testAccMachineExtensionsConfig(name, talosVersion, kubernetesVersion, extensions string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_cluster" "test" {
  name               = %q
  kubernetes_version = %q
  talos_version      = %q
}

resource "omni_machine_set" "workers" {
  cluster = omni_cluster.test.name
  role    = "workers"
}

resource "omni_machine_extensions" "cluster" {
  cluster    = omni_cluster.test.name
  extensions = [%s]
}

resource "omni_machine_extensions" "workers" {
  cluster = omni_cluster.test.name

  selector = {
    machine_set = omni_machine_set.workers.name
  }

  extensions = [%s]
}
`, name, kubernetesVersion, talosVersion, extensions, extensions)
}

func testAccMachineExtensionsConfigConflicting() string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_machine_extensions" "conflicting" {
  cluster = "some-cluster"

  selector = {
    machine_set     = "some-machine-set"
    cluster_machine = "some-machine-uuid"
  }

  extensions = [%q]
}
`, helloWorldExtension)
}
