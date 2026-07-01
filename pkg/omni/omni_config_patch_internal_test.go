// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

func TestConfigPatchID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patch    string
		expected string
		weight   int64
	}{
		{name: "default weight", patch: "foo", weight: 400, expected: "400-test-cluster-foo"},
		{name: "low weight is zero padded", patch: "bar", weight: 50, expected: "050-test-cluster-bar"},
		{name: "name with dashes", patch: "a-b-c", weight: 200, expected: "200-test-cluster-a-b-c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := configPatchID(tc.weight, "test-cluster", tc.patch); got != tc.expected {
				t.Fatalf("configPatchID(%d, %q) = %q, want %q", tc.weight, tc.patch, got, tc.expected)
			}
		})
	}
}

func TestParseConfigPatchID(t *testing.T) {
	for _, tc := range []struct {
		id           string
		expectedName string
		expectedWt   int64
	}{
		{id: "400-foo", expectedWt: 400, expectedName: "foo"},
		{id: "050-bar", expectedWt: 50, expectedName: "bar"},
		{id: "200-a-b-c", expectedWt: 200, expectedName: "a-b-c"},
		{id: "no-weight-prefix", expectedWt: 0, expectedName: "no-weight-prefix"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			weight, name := parseConfigPatchID(tc.id)
			if weight != tc.expectedWt || name != tc.expectedName {
				t.Fatalf("parseConfigPatchID(%q) = (%d, %q), want (%d, %q)", tc.id, weight, name, tc.expectedWt, tc.expectedName)
			}
		})
	}
}

func TestConfigPatchApplyData(t *testing.T) {
	r := &configPatchResource{}

	t.Run("valid patch is stored", func(t *testing.T) {
		patch := omni.NewConfigPatch("400-valid")

		plan := configPatchResourceModel{
			Data: types.StringValue("machine:\n  kubelet:\n    extraArgs:\n      max-pods: \"150\"\n"),
		}

		if err := r.applyData(plan, patch); err != nil {
			t.Fatalf("applyData returned unexpected error: %v", err)
		}

		buffer, err := patch.TypedSpec().Value.GetUncompressedData()
		if err != nil {
			t.Fatalf("failed to read back patch data: %v", err)
		}

		defer buffer.Free()

		if len(buffer.Data()) == 0 {
			t.Fatal("expected patch data to be stored")
		}
	})

	t.Run("forbidden field is rejected", func(t *testing.T) {
		patch := omni.NewConfigPatch("400-forbidden")

		plan := configPatchResourceModel{
			Data: types.StringValue("cluster:\n  id: should-not-be-allowed\n"),
		}

		if err := r.applyData(plan, patch); err == nil {
			t.Fatal("expected applyData to reject an Omni-controlled field, got nil error")
		}
	})
}
