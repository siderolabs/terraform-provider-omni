// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"testing"

	"github.com/cosi-project/runtime/pkg/safe"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/google/uuid"
	tfresource "github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	omnires "github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"github.com/stretchr/testify/require"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// TestAccOmniMachineLabelsResource exercises the labels of a machine that already carries one the
// resource does not manage, standing in for a label seeded from a join token: the resource adds its
// own labels next to it, updates and removes only those, puts back a managed label changed outside
// Terraform, and leaves the foreign label behind when destroyed.
//
// Omni accepts labels for a machine that has not joined, so the test uses a random UUID rather
// than a real machine.
func TestAccOmniMachineLabelsResource(t *testing.T) {
	machineID := uuid.NewString()
	seeded := map[string]string{"seeded": "token"}

	tfresource.ParallelTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckMachineLabels(machineID, seeded),
		Steps: []tfresource.TestStep{
			{ // add labels next to the seeded one
				PreConfig: func() {
					testAccSetMachineLabels(t, machineID, seeded)
				},
				Config: testAccMachineLabelsConfig(machineID, map[string]string{"a": "1", "b": "2"}),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("omni_machine_labels.test", "machine_id", machineID),
					tfresource.TestCheckResourceAttr("omni_machine_labels.test", "labels.%", "2"),
					tfresource.TestCheckResourceAttr("omni_machine_labels.test", "labels.a", "1"),
					testAccCheckMachineLabels(machineID, map[string]string{"seeded": "token", "a": "1", "b": "2"}),
				),
			},
			{ // change one label and drop the other, in place
				Config: testAccMachineLabelsConfig(machineID, map[string]string{"a": "3"}),
				Check: tfresource.ComposeAggregateTestCheckFunc(
					tfresource.TestCheckResourceAttr("omni_machine_labels.test", "labels.%", "1"),
					tfresource.TestCheckResourceAttr("omni_machine_labels.test", "labels.a", "3"),
					testAccCheckMachineLabels(machineID, map[string]string{"seeded": "token", "a": "3"}),
				),
			},
			{ // a managed label changed outside Terraform is drift, and gets put back
				PreConfig: func() {
					testAccSetMachineLabels(t, machineID, map[string]string{"a": "changed"})
				},
				Config: testAccMachineLabelsConfig(machineID, map[string]string{"a": "3"}),
				Check:  testAccCheckMachineLabels(machineID, map[string]string{"seeded": "token", "a": "3"}),
			},
			{ // an import adopts every label on the machine
				ResourceName:  "omni_machine_labels.test",
				ImportState:   true,
				ImportStateId: machineID,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported resource, got %d", len(states))
					}

					attrs := states[0].Attributes

					if attrs["machine_id"] != machineID || attrs["labels.%"] != "2" || attrs["labels.a"] != "3" || attrs["labels.seeded"] != "token" {
						return fmt.Errorf("unexpected imported attributes: %v", attrs)
					}

					return nil
				},
			},
		},
	})
}

// TestAccOmniMachineLabelsResourceRemovesEmpty asserts that destroying the resource that set every
// label of a machine removes the machine's labels resource altogether, as the UI does.
func TestAccOmniMachineLabelsResourceRemovesEmpty(t *testing.T) {
	machineID := uuid.NewString()

	tfresource.ParallelTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckMachineLabels(machineID, nil),
		Steps: []tfresource.TestStep{
			{
				Config: testAccMachineLabelsConfig(machineID, map[string]string{"role": "worker"}),
				Check:  testAccCheckMachineLabels(machineID, map[string]string{"role": "worker"}),
			},
		},
	})
}

// TestAccOmniMachineLabelsResourceRetriesDestroy asserts that destroying the resource finishes a
// teardown left behind by an earlier attempt, which failed after tearing the labels down but before
// destroying them.
func TestAccOmniMachineLabelsResourceRetriesDestroy(t *testing.T) {
	machineID := uuid.NewString()
	labels := map[string]string{"role": "worker"}

	tfresource.ParallelTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckMachineLabels(machineID, nil),
		Steps: []tfresource.TestStep{
			{
				Config: testAccMachineLabelsConfig(machineID, labels),
				Check:  testAccCheckMachineLabels(machineID, labels),
			},
			{ // the labels are torn down outside Terraform, and the destroy has to finish the job
				PreConfig: func() {
					testAccTearDownMachineLabels(t, machineID)
				},
				Config:  testAccMachineLabelsConfig(machineID, labels),
				Destroy: true,
			},
		},
	})
}

// TestAccOmniMachineLabelsImportNothingToAdopt asserts that importing a machine whose labels
// resource holds no label, as Omni leaves it after recording the platform tags, says so.
func TestAccOmniMachineLabelsImportNothingToAdopt(t *testing.T) {
	machineID := uuid.NewString()

	tfresource.ParallelTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{
			{
				PreConfig: func() {
					testAccSetMachineLabels(t, machineID, nil)
				},
				Config:        testAccMachineLabelsConfig(machineID, map[string]string{"role": "worker"}),
				ResourceName:  "omni_machine_labels.test",
				ImportState:   true,
				ImportStateId: machineID,
				ExpectError:   regexp.MustCompile("No machine labels to import"),
			},
		},
	})
}

// TestAccOmniMachineLabelsReservedPrefix asserts that a label in Omni's own namespace is rejected at
// plan time.
func TestAccOmniMachineLabelsReservedPrefix(t *testing.T) {
	tfresource.ParallelTest(t, tfresource.TestCase{
		ProtoV6ProviderFactories: omni.TestAccProtoV6ProviderFactories,
		Steps: []tfresource.TestStep{
			{
				Config:      testAccMachineLabelsConfig(uuid.NewString(), map[string]string{"omni.sidero.dev/cluster": "x"}),
				ExpectError: regexp.MustCompile("Invalid machine labels"),
			},
		},
	})
}

func testAccMachineLabelsConfig(machineID string, labels map[string]string) string {
	var entries strings.Builder

	for key, value := range labels {
		fmt.Fprintf(&entries, "    %q = %q\n", key, value)
	}

	return fmt.Sprintf(`
provider "omni" {
  insecure_skip_tls_verify = true
}

resource "omni_machine_labels" "test" {
  machine_id = %q

  labels = {
%s  }
}
`, machineID, entries.String())
}

// testAccSetMachineLabels sets labels on the machine behind Terraform's back, and removes the
// machine's labels resource once the test is done.
func testAccSetMachineLabels(t *testing.T, machineID string, labels map[string]string) {
	t.Helper()

	client, err := newTestClient()
	require.NoError(t, err)

	defer client.Close() //nolint:errcheck

	err = safe.StateModify(t.Context(), client.Omni().State(), omnires.NewMachineLabels(machineID), func(res *omnires.MachineLabels) error {
		for key, value := range labels {
			res.Metadata().Labels().Set(key, value)
		}

		return nil
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupClient, cleanupErr := newTestClient()
		if cleanupErr != nil {
			return
		}

		defer cleanupClient.Close() //nolint:errcheck

		cleanupErr = cleanupClient.Omni().State().TeardownAndDestroy(context.Background(), omnires.NewMachineLabels(machineID).Metadata())
		if cleanupErr != nil && !cosistate.IsNotFoundError(cleanupErr) {
			t.Logf("failed to remove the labels of machine %q: %s", machineID, cleanupErr)
		}
	})
}

// testAccTearDownMachineLabels puts the machine's labels into the tearing down phase, as an
// interrupted destroy leaves them.
func testAccTearDownMachineLabels(t *testing.T, machineID string) {
	t.Helper()

	client, err := newTestClient()
	require.NoError(t, err)

	defer client.Close() //nolint:errcheck

	_, err = client.Omni().State().Teardown(t.Context(), omnires.NewMachineLabels(machineID).Metadata())
	require.NoError(t, err)
}

// testAccCheckMachineLabels asserts, via the live Omni API, the full label set of the machine. With
// no labels expected, the machine must have no labels resource at all.
func testAccCheckMachineLabels(machineID string, expected map[string]string) tfresource.TestCheckFunc {
	return func(*terraform.State) error {
		client, err := newTestClient()
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck

		machineLabels, err := safe.ReaderGetByID[*omnires.MachineLabels](context.Background(), client.Omni().State(), machineID)

		switch {
		case cosistate.IsNotFoundError(err) && len(expected) == 0:
			return nil
		case err != nil:
			return fmt.Errorf("failed to read the labels of machine %q: %w", machineID, err)
		}

		if got := machineLabels.Metadata().Labels().Raw(); !maps.Equal(got, expected) {
			return fmt.Errorf("unexpected labels on machine %q: got %v, want %v", machineID, got, expected)
		}

		return nil
	}
}
