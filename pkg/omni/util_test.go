// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"testing"

	"github.com/siderolabs/omni/client/api/omni/specs"
	"github.com/stretchr/testify/assert"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

func TestEnumDisplayName(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "screaming snake", value: "ACTIVE", want: "Active"},
		{name: "the zero value", value: "UNKNOWN", want: "Unknown"},
		{
			// Underscores are dropped rather than kept, so a multi-word value reads like the
			// CamelCase enums Omni also ships.
			name:  "multi-word screaming snake",
			value: "PENDING_REVOCATION",
			want:  "PendingRevocation",
		},
		{
			// Already in the target shape: touching it would produce "Unset" from "UNSET" but
			// "Rollingupdate" from "RollingUpdate", so anything not fully uppercase is left alone.
			name:  "camel case is passed through",
			value: "Rolling",
			want:  "Rolling",
		},
		{name: "multi-word camel case is passed through", value: "RollingUpdate", want: "RollingUpdate"},
		{name: "empty", value: "", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, omni.EnumDisplayName(test.value))
		})
	}
}

func TestEnumName(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		nameMap map[int32]string
		name    string
		want    string
		number  int32
	}{
		{
			name:    "a known join token state",
			nameMap: specs.JoinTokenStatusSpec_State_name,
			number:  int32(specs.JoinTokenStatusSpec_ACTIVE),
			want:    "Active",
		},
		{
			// A state Omni gained after this provider was built must not come out as an empty
			// string, which would read as "no state" rather than "a state we do not know".
			name:    "an unknown join token state falls back to the zero value",
			nameMap: specs.JoinTokenStatusSpec_State_name,
			number:  9999,
			want:    "Unknown",
		},
		{
			name:    "a known machine set strategy",
			nameMap: specs.MachineSetSpec_UpdateStrategy_name,
			number:  int32(specs.MachineSetSpec_Rolling),
			want:    "Rolling",
		},
		{
			name:    "an unknown machine set strategy falls back to the zero value",
			nameMap: specs.MachineSetSpec_UpdateStrategy_name,
			number:  9999,
			want:    "Unset",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, omni.EnumName(test.nameMap, test.number))
		})
	}
}

func TestEnumNames(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		nameMap map[int32]string
		name    string
		want    []string
	}{
		{
			name:    "join token states, in enum order",
			nameMap: specs.JoinTokenStatusSpec_State_name,
			want:    []string{"Unknown", "Active", "Revoked", "Expired"},
		},
		{
			// Omni writes this enum in CamelCase already, and these are values a user puts in a
			// configuration, so the generated spelling has to survive untouched.
			name:    "machine set strategies keep their casing",
			nameMap: specs.MachineSetSpec_UpdateStrategy_name,
			want:    []string{"Unset", "Rolling"},
		},
		{
			// No acceptance test reaches allocation_type, since it needs a machine class with
			// machines behind it, so this is the only guard that the released spelling survives
			// being derived.
			name:    "machine set allocation types keep their casing",
			nameMap: specs.MachineSetSpec_MachineAllocation_Type_name,
			want:    []string{"Static", "Unlimited"},
		},
		{
			// Go randomizes map iteration, which would reshuffle the generated documentation on
			// every run.
			name:    "ordered by enum number, not by map order",
			nameMap: map[int32]string{2: "THIRD", 0: "FIRST", 1: "SECOND"},
			want:    []string{"First", "Second", "Third"},
		},
		{name: "empty", nameMap: map[int32]string{}, want: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, omni.EnumNames(test.nameMap))
		})
	}
}

func TestDescribeOneOf(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		want  string
		names []string
	}{
		{
			name:  "several values",
			names: []string{"Unknown", "Active"},
			want:  "One of: `Unknown`, `Active`.",
		},
		{name: "a single value", names: []string{"Rolling"}, want: "One of: `Rolling`."},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, omni.DescribeOneOf(test.names))
		})
	}
}
