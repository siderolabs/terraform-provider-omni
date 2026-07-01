// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/gen/pair"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

func TestMachineExtensionsTargetSuffix(t *testing.T) {
	r := &machineExtensionsResource{}

	for _, tc := range []struct {
		name     string
		model    machineExtensionsResourceModel
		expected string
	}{
		{
			name:     "cluster scope",
			model:    machineExtensionsResourceModel{Cluster: types.StringValue("prod")},
			expected: "prod",
		},
		{
			name:     "machine set scope",
			model:    machineExtensionsResourceModel{Cluster: types.StringValue("prod"), Selector: &machineExtensionsSelectorModel{MachineSet: types.StringValue("prod-workers")}},
			expected: "prod-workers",
		},
		{
			name: "cluster machine scope wins over machine set",
			model: machineExtensionsResourceModel{
				Cluster: types.StringValue("prod"),
				Selector: &machineExtensionsSelectorModel{
					MachineSet:     types.StringValue("prod-workers"),
					ClusterMachine: types.StringValue("uuid-1"),
				},
			},
			expected: "uuid-1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suffix := r.targetSuffix(tc.model)
			if suffix != tc.expected {
				t.Fatalf("targetSuffix() = %q, want %q", suffix, tc.expected)
			}

			if got, want := machineExtensionsID(suffix), "schematic-"+tc.expected; got != want {
				t.Fatalf("machineExtensionsID() = %q, want %q", got, want)
			}
		})
	}
}

func TestMachineExtensionsScopeLabels(t *testing.T) {
	r := &machineExtensionsResource{}

	for _, tc := range []struct {
		name     string
		model    machineExtensionsResourceModel
		expected []pair.Pair[string, string]
	}{
		{
			name:  "cluster scope sets only the cluster label",
			model: machineExtensionsResourceModel{Cluster: types.StringValue("prod")},
			expected: []pair.Pair[string, string]{
				pair.MakePair(omni.LabelCluster, "prod"),
			},
		},
		{
			name:  "machine set scope adds the machine set label",
			model: machineExtensionsResourceModel{Cluster: types.StringValue("prod"), Selector: &machineExtensionsSelectorModel{MachineSet: types.StringValue("prod-workers")}},
			expected: []pair.Pair[string, string]{
				pair.MakePair(omni.LabelCluster, "prod"),
				pair.MakePair(omni.LabelMachineSet, "prod-workers"),
			},
		},
		{
			name:  "cluster machine scope adds the cluster machine label",
			model: machineExtensionsResourceModel{Cluster: types.StringValue("prod"), Selector: &machineExtensionsSelectorModel{ClusterMachine: types.StringValue("uuid-1")}},
			expected: []pair.Pair[string, string]{
				pair.MakePair(omni.LabelCluster, "prod"),
				pair.MakePair(omni.LabelClusterMachine, "uuid-1"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := r.scopeLabels(tc.model)

			if len(got) != len(tc.expected) {
				t.Fatalf("scopeLabels() = %v, want %v", got, tc.expected)
			}

			for i := range got {
				if got[i] != tc.expected[i] {
					t.Fatalf("scopeLabels()[%d] = %v, want %v", i, got[i], tc.expected[i])
				}
			}
		})
	}
}
