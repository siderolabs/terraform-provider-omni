// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build qemu

// This file holds the end-to-end test that requires real machines and is therefore guarded by the
// `qemu` build tag. It is compiled and run only by the QEMU harness (hack/test/run-qemu.sh, via
// `go test -tags qemu`), keeping the machine-less acceptance suite unaffected.

package integration_test

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"testing"
	"text/template"
	"time"

	"github.com/blang/semver/v4"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
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

//go:embed testdata/cluster_with_machines.tf.tmpl
var clusterWithMachinesTemplateSource string

// clusterWithMachinesTemplate renders a cluster with a control plane and worker machine set and one
// machine_set_node per allocated machine.
var clusterWithMachinesTemplate = template.Must(template.New("cluster").Parse(clusterWithMachinesTemplateSource))

// TestAccOmniClusterWithMachines is the end-to-end test that exercises a cluster backed by real
// machines. It requires four machines to have already joined the target Omni instance (provisioned
// as QEMU VMs by hack/test/run-qemu.sh).
//
// It walks the cluster through scale transitions, waiting for it to settle after each:
//  1. bootstrap with a single control plane,
//  2. scale up to three control planes and one worker,
//  3. scale back down to a single control plane (keeping the worker),
//
// then tears it down.
func TestAccOmniClusterWithMachines(t *testing.T) {
	talosVersion, kubernetesVersion := discoverVersions(t)

	// Three control plane machines plus one worker are required to exercise the full scale up/down.
	machines := availableMachineIDs(t, 4)
	controlPlanes, worker := machines[:3], machines[3:4]

	name := acctest.RandomWithPrefix("tf-acc-qemu")

	step := func(cps, workers []string) tfresource.TestStep {
		return tfresource.TestStep{
			Config: renderClusterWithMachinesConfig(t, name, talosVersion, kubernetesVersion, cps, workers),
			Check: tfresource.ComposeAggregateTestCheckFunc(
				tfresource.TestCheckResourceAttr("omni_cluster.test", "name", name),
				testAccCheckClusterRunning(t.Context(), name, len(cps)+len(workers), clusterReadyTimeout),
			),
		}
	}

	tfresource.UnitTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckClusterDestroy,
		Steps: []tfresource.TestStep{
			step(controlPlanes[:1], nil),    // bootstrap with a single control plane
			step(controlPlanes, worker),     // scale up to three control planes and one worker
			step(controlPlanes[:1], worker), // scale down to a single control plane and one worker
		},
	})
}

func renderClusterWithMachinesConfig(t *testing.T, name, talosVersion, kubernetesVersion string, controlPlanes, workers []string) string {
	t.Helper()

	var buf bytes.Buffer

	if err := clusterWithMachinesTemplate.Execute(&buf, struct {
		Name              string
		TalosVersion      string
		KubernetesVersion string
		ControlPlanes     []string
		Workers           []string
	}{
		Name:              name,
		TalosVersion:      talosVersion,
		KubernetesVersion: kubernetesVersion,
		ControlPlanes:     controlPlanes,
		Workers:           workers,
	}); err != nil {
		t.Fatalf("failed to render cluster config: %v", err)
	}

	return buf.String()
}

// availableMachineIDs polls the target Omni until at least count machines are connected and
// available for allocation, returning their UUIDs. It fails the test if the machines do not appear
// within the deadline.
func availableMachineIDs(t *testing.T, count int) []string {
	t.Helper()

	client, err := newTestClient()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()

	for {
		machines, err := safe.ReaderListAll[*omnires.MachineStatus](
			ctx,
			client.Omni().State(),
			cosistate.WithLabelQuery(
				resource.LabelExists(omnires.MachineStatusLabelAvailable),
				resource.LabelExists(omnires.MachineStatusLabelConnected),
			),
		)
		if err != nil {
			t.Fatalf("failed to list available machines: %v", err)
		}

		ids := make([]string, 0, machines.Len())
		for machine := range machines.All() {
			ids = append(ids, machine.Metadata().ID())
		}

		if len(ids) >= count {
			return ids[:count]
		}

		select {
		case <-ctx.Done():
			t.Fatalf("only %d of %d required machines became available before the deadline", len(ids), count)
		case <-time.After(10 * time.Second):
		}
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
