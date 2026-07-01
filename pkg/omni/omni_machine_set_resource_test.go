// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// TestAccOmniMachineSetResource exercises a cluster plus its control plane and worker machine sets,
// a cluster-scoped config patch, and the single-control-plane guard.
func TestAccOmniMachineSetResource(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc")

	resource.ParallelTest(t, resource.TestCase{
		PreCheck:                 func() { waitForVersionsReady(t, talosVersion, kubernetesVersion) },
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckClusterDestroy,
		Steps: []resource.TestStep{
			{ // create cluster + machine sets + config patch
				Config: testAccMachineSetConfig(name, talosVersion, kubernetesVersion),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("omni_machine_set.cp", "role", "controlplane"),
					resource.TestCheckResourceAttr("omni_machine_set.default-workers", "role", "workers"),
					resource.TestCheckResourceAttr("omni_config_patch.kubelet", "id", "400-"+name+"-kubelet"),
				),
			},
			{ // a second control plane machine set is rejected
				Config:      testAccMachineSetConfigSecondControlPlane(name, talosVersion, kubernetesVersion),
				ExpectError: regexp.MustCompile("AlreadyExists"),
			},
			{ // a second default worker machine set is rejected
				Config:      testAccMachineSetConfigSecondDefaultWorkers(name, talosVersion, kubernetesVersion),
				ExpectError: regexp.MustCompile("AlreadyExists"),
			},
			{ // a malformed control plane machine set is rejected
				Config:      testAccMachineSetConfigMalformedCP(name, talosVersion, kubernetesVersion),
				ExpectError: regexp.MustCompile("cannot be set when"),
			},
			{ // reset back to the initial state
				Config: testAccMachineSetConfig(name, talosVersion, kubernetesVersion),
			},
		},
	})
}

func testAccMachineSetConfigSecondDefaultWorkers(name, talosVersion, kubernetesVersion string) string {
	return testAccMachineSetConfig(name, talosVersion, kubernetesVersion) + `
resource "omni_machine_set" "workers2" {
  cluster = omni_cluster.test.name
  role    = "workers"
}
`
}

func testAccMachineSetConfigSecondControlPlane(name, talosVersion, kubernetesVersion string) string {
	return testAccMachineSetConfig(name, talosVersion, kubernetesVersion) + `
resource "omni_machine_set" "cp2" {
  cluster = omni_cluster.test.name
  role    = "controlplane"
}
`
}

func testAccMachineSetConfig(name, talosVersion, kubernetesVersion string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_cluster" "test" {
  name               = %q
  kubernetes_version = %q
  talos_version      = %q
}

resource "omni_machine_set" "cp" {
  cluster = omni_cluster.test.name
  role    = "controlplane"
}

resource "omni_machine_set" "default-workers" {
  cluster = omni_cluster.test.name
  role    = "workers"
}

resource "omni_config_patch" "kubelet" {
  name    = "kubelet"
  cluster = omni_cluster.test.name

  data = yamlencode({
    machine = {
      kubelet = {
        extraArgs = {
          "max-pods" = "150"
        }
      }
    }
  })
}
`, name, kubernetesVersion, talosVersion)
}

func testAccMachineSetConfigMalformedCP(name, talosVersion, kubernetesVersion string) string {
	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_cluster" "test" {
  name               = %q
  kubernetes_version = %q
  talos_version      = %q
}

resource "omni_machine_set" "cp" {
  name    = "control-planes"
  cluster = omni_cluster.test.name
  role    = "controlplane"
}

resource "omni_machine_set" "workers" {
  cluster = omni_cluster.test.name
  role    = "workers"
}
`, name, kubernetesVersion, talosVersion)
}
