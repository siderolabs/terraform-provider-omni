// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build proxmox

// This file holds the end-to-end test that requires real machines and is therefore guarded by the
// `proxmox` build tag. It is compiled and run only by the Proxmox harness (hack/test/run-proxmox.sh,
// via `go test -tags proxmox`), keeping the machine-less acceptance suite unaffected.

package integration_test

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"text/template"
	"time"

	"github.com/blang/semver/v4"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/hashicorp/terraform-plugin-testing/config"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/siderolabs/omni/client/api/omni/specs"
	omnires "github.com/siderolabs/omni/client/pkg/omni/resources/omni"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// clusterReadyTimeout bounds how long each cluster transition (bootstrap, scale up, scale down) may
// take to settle. Talos installs, etcd membership changes and Kubernetes bring-up are slow.
const clusterReadyTimeout = 20 * time.Minute

// machineLabelKey is the label each VM's machine is identified by in Omni.
const machineLabelKey = "terraform/machine"

//go:embed testdata/cluster_with_machines.tf.tmpl
var clusterWithMachinesTemplateSource string

// clusterWithMachinesTemplate renders the Proxmox VMs, a cluster with a control plane and worker
// machine set, one machine_set_node per VM, and the wait for the cluster to settle.
var clusterWithMachinesTemplate = template.Must(template.New("cluster").Parse(clusterWithMachinesTemplateSource))

// TestAccOmniClusterWithMachines is the end-to-end test that exercises a cluster backed by real
// machines. It creates them as Proxmox VMs the way examples/modules/omni-proxmox-cluster does, which
// is also what it tests: each VM joins Omni with a label signed into its join token,
// omni_machine_wait resolves the machine's UUID from that label, and omni_cluster_wait waits for the
// cluster after every change.
//
// It walks the cluster through scale transitions, creating and destroying VMs along the way:
//  1. bootstrap with a single control plane,
//  2. scale up to three control planes and one worker,
//  3. scale back down to a single control plane (keeping the worker),
//
// then tears it down.
func TestAccOmniClusterWithMachines(t *testing.T) {
	talosVersion, kubernetesVersion := discoverVersions(t)

	controlPlanes := []string{"cp-0", "cp-1", "cp-2"}
	worker := []string{"worker-0"}

	name := acctest.RandomWithPrefix("tf-acc-pve")

	step := func(cps, workers []string) tfresource.TestStep {
		return tfresource.TestStep{
			// A configuration file rather than an inline one: the test framework only runs
			// `terraform init` on the step's own configuration when it comes from a file, and that
			// is what installs the Proxmox provider. The file configures its providers, so the
			// provider under test has to be given per step as well.
			ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
			ConfigFile:               config.StaticFile(renderClusterWithMachinesConfig(t, name, talosVersion, kubernetesVersion, cps, workers)),
			Check: tfresource.ComposeAggregateTestCheckFunc(
				tfresource.TestCheckResourceAttr("omni_cluster.test", "name", name),
				// A named machine set keeps the configured name and exposes the cluster-prefixed Omni
				// resource ID separately, so a re-apply converges (regression for #14).
				tfresource.TestCheckResourceAttr("omni_machine_set.extra-workers", "name", "extra-workers"),
				tfresource.TestCheckResourceAttr("omni_machine_set.extra-workers", "id", name+"-extra-workers"),
				tfresource.TestCheckResourceAttr("omni_machine_set.workers", "name", "workers"),
				tfresource.TestCheckResourceAttr("omni_machine_set.workers", "id", name+"-workers"),
				tfresource.TestCheckResourceAttr("omni_machine_set.cp", "id", name+"-control-planes"),
				// The apply only finishes once omni_cluster_wait has seen the cluster settle.
				tfresource.TestCheckResourceAttr("omni_cluster_wait.test", "ready", "true"),
				tfresource.TestCheckResourceAttr("omni_cluster_wait.test", "phase", "Running"),
				tfresource.TestCheckResourceAttr("omni_cluster_wait.test", "machines_total", fmt.Sprint(len(cps)+len(workers))),
				tfresource.TestCheckResourceAttr("omni_cluster_wait.test", "machines_healthy", fmt.Sprint(len(cps)+len(workers))),
				// Cross-check both waits against Omni directly: the cluster has settled with exactly
				// the expected machines, and each machine is the one its VM's label points at.
				testAccCheckClusterRunning(t.Context(), name, len(cps)+len(workers), 2*time.Minute),
				testAccCheckMachinesMatchLabels(t.Context()),
			),
		}
	}

	tfresource.UnitTest(t, tfresource.TestCase{
		CheckDestroy: testAccCheckClusterDestroy,
		Steps: []tfresource.TestStep{
			step(controlPlanes[:1], nil),    // bootstrap with a single control plane
			step(controlPlanes, worker),     // scale up to three control planes and one worker
			step(controlPlanes[:1], worker), // scale down to a single control plane and one worker
		},
	})
}

// renderClusterWithMachinesConfig renders a step's configuration to a file and returns its path.
func renderClusterWithMachinesConfig(t *testing.T, name, talosVersion, kubernetesVersion string, controlPlanes, workers []string) string {
	t.Helper()

	var buf bytes.Buffer

	if err := clusterWithMachinesTemplate.Execute(&buf, struct {
		Proxmox           proxmoxConfig
		Name              string
		TalosVersion      string
		KubernetesVersion string
		ReadyTimeout      string
		ControlPlanes     []string
		Workers           []string
		Machines          []string
	}{
		Proxmox:           proxmox,
		Name:              name,
		TalosVersion:      talosVersion,
		KubernetesVersion: kubernetesVersion,
		ReadyTimeout:      clusterReadyTimeout.String(),
		ControlPlanes:     controlPlanes,
		Workers:           workers,
		Machines:          slices.Concat(controlPlanes, workers),
	}); err != nil {
		t.Fatalf("failed to render the cluster configuration: %v", err)
	}

	path := filepath.Join(t.TempDir(), "main.tf")

	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("failed to write the cluster configuration: %v", err)
	}

	return path
}

// testAccCheckMachinesMatchLabels checks that the machine each VM was resolved to is the machine
// that joined with that VM's label, i.e. that omni_machine_wait found the right machine. The label
// is read back from Omni, where it got from the join token.
func testAccCheckMachinesMatchLabels(ctx context.Context) tfresource.TestCheckFunc {
	return func(s *terraform.State) error {
		client, err := newTestClient()
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck

		var checked int

		for key, rs := range s.RootModule().Resources {
			if rs.Type != "omni_machine_wait" {
				continue
			}

			machineID := rs.Primary.Attributes["machine_id"]
			want := rs.Primary.Attributes["labels."+machineLabelKey]

			status, err := safe.ReaderGetByID[*omnires.MachineStatus](ctx, client.Omni().State(), machineID)
			if err != nil {
				return fmt.Errorf("machine %q of %s: %w", machineID, key, err)
			}

			if got, _ := status.Metadata().Labels().Get(machineLabelKey); got != want {
				return fmt.Errorf("machine %q of %s has %s=%q, want %q", machineID, key, machineLabelKey, got, want)
			}

			checked++
		}

		if checked == 0 {
			return fmt.Errorf("no omni_machine_wait resources in the state")
		}

		return nil
	}
}

// testAccCheckClusterRunning polls until the cluster reports Running and Ready with the expected
// number of healthy machines, and every allocated machine is itself Ready and in the Running stage.
// This ensures a scale transition has fully settled.
func testAccCheckClusterRunning(ctx context.Context, name string, expectedMachines int, timeout time.Duration) tfresource.TestCheckFunc {
	return func(*terraform.State) error {
		client, err := newTestClient()
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck

		pollCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		for {
			ready, err := clusterReady(pollCtx, client.Omni().State(), name, expectedMachines)
			if err != nil {
				return err
			}

			if ready {
				return nil
			}

			select {
			case <-pollCtx.Done():
				return fmt.Errorf("cluster %q did not reach Running with %d healthy machines within %s", name, expectedMachines, timeout)
			case <-time.After(15 * time.Second):
			}
		}
	}
}

// clusterReady reports whether the cluster is Running/Ready with exactly expectedMachines machines,
// all of which are Ready and in the Running stage.
func clusterReady(ctx context.Context, state cosistate.State, name string, expectedMachines int) (bool, error) {
	status, err := safe.ReaderGetByID[*omnires.ClusterStatus](ctx, state, name)
	if err != nil {
		if cosistate.IsNotFoundError(err) {
			return false, nil
		}

		return false, fmt.Errorf("failed to read cluster status %q: %w", name, err)
	}

	value := status.TypedSpec().Value
	if value.GetPhase() != specs.ClusterStatusSpec_RUNNING ||
		!value.GetReady() ||
		value.GetMachines().GetHealthy() != uint32(expectedMachines) ||
		value.GetMachines().GetTotal() != uint32(expectedMachines) {
		return false, nil
	}

	machines, err := safe.ReaderListAll[*omnires.ClusterMachineStatus](
		ctx,
		state,
		cosistate.WithLabelQuery(resource.LabelEqual(omnires.LabelCluster, name)),
	)
	if err != nil {
		return false, fmt.Errorf("failed to list cluster machine statuses for %q: %w", name, err)
	}

	if machines.Len() != expectedMachines {
		return false, nil
	}

	for machine := range machines.All() {
		spec := machine.TypedSpec().Value
		if !spec.GetReady() || spec.GetStage() != specs.ClusterMachineStatusSpec_RUNNING {
			return false, nil
		}
	}

	return true, nil
}

// discoverVersions returns the highest non-deprecated Talos version available on the target Omni
// instance together with its highest compatible Kubernetes version. The test fails when the
// instance advertises no usable versions.
func discoverVersions(t *testing.T) (talosVersion, kubernetesVersion string) {
	t.Helper()

	client, err := newTestClient()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close() //nolint:errcheck

	// Omni syncs the available Talos versions from the image factory shortly after start-up, so a
	// freshly-booted instance may not advertise any yet. Poll for a bounded window before giving up.
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	for {
		bestTalos, bestK8s, found := highestTalosVersion(ctx, t, client.Omni().State())
		if found {
			return bestTalos.String(), bestK8s.String()
		}

		select {
		case <-ctx.Done():
			t.Fatal("no usable Talos versions advertised by the target Omni instance")
		case <-time.After(5 * time.Second):
		}
	}
}

// highestTalosVersion returns the highest non-deprecated Talos version advertised by the instance
// together with its highest compatible Kubernetes version.
func highestTalosVersion(ctx context.Context, t *testing.T, state cosistate.State) (talos, kubernetes semver.Version, found bool) {
	t.Helper()

	versions, err := safe.ReaderListAll[*omnires.TalosVersion](ctx, state)
	if err != nil {
		t.Fatalf("failed to list Talos versions: %v", err)
	}

	for version := range versions.All() {
		spec := version.TypedSpec().Value
		if spec.GetDeprecated() || spec.GetUnsupported() || len(spec.GetCompatibleKubernetesVersions()) == 0 {
			continue
		}

		talosVersion, err := semver.ParseTolerant(version.Metadata().ID())
		if err != nil {
			continue
		}

		k8s, ok := highestSemver(spec.GetCompatibleKubernetesVersions())
		if !ok {
			continue
		}

		if !found || talosVersion.GT(talos) {
			talos, kubernetes, found = talosVersion, k8s, true
		}
	}

	return talos, kubernetes, found
}

// highestSemver returns the highest version from the list parsed as semver.
func highestSemver(versions []string) (semver.Version, bool) {
	var (
		best  semver.Version
		found bool
	)

	for _, raw := range versions {
		v, err := semver.ParseTolerant(raw)
		if err != nil {
			continue
		}

		if !found || v.GT(best) {
			best, found = v, true
		}
	}

	return best, found
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
