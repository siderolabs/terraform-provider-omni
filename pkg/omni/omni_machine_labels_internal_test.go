// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"testing"

	omnires "github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"github.com/stretchr/testify/assert"
)

func TestUpdateManagedLabels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		existing map[string]string
		previous map[string]string
		desired  map[string]string
		expected map[string]string
		name     string
	}{
		{
			name:     "adds to the labels already on the machine",
			existing: map[string]string{"seeded": "token"},
			desired:  map[string]string{"role": "worker"},
			expected: map[string]string{"seeded": "token", "role": "worker"},
		},
		{
			name:     "takes over a key already on the machine",
			existing: map[string]string{"role": "controlplane"},
			desired:  map[string]string{"role": "worker"},
			expected: map[string]string{"role": "worker"},
		},
		{
			name:     "removes only the managed keys dropped from the config",
			existing: map[string]string{"seeded": "token", "role": "worker", "zone": "a"},
			previous: map[string]string{"role": "worker", "zone": "a"},
			desired:  map[string]string{"role": "controlplane"},
			expected: map[string]string{"seeded": "token", "role": "controlplane"},
		},
		{
			name:     "removing every managed key keeps the rest",
			existing: map[string]string{"seeded": "token", "role": "worker"},
			previous: map[string]string{"role": "worker"},
			expected: map[string]string{"seeded": "token"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			machineLabels := omnires.NewMachineLabels("machine")

			for key, value := range tc.existing {
				machineLabels.Metadata().Labels().Set(key, value)
			}

			updateManagedLabels(machineLabels.Metadata().Labels(), tc.previous, tc.desired)

			assert.Equal(t, tc.expected, machineLabels.Metadata().Labels().Raw())
		})
	}
}

func TestManagedLabels(t *testing.T) {
	t.Parallel()

	current := map[string]string{"seeded": "token", "role": "controlplane"}

	assert.Equal(t,
		map[string]string{"role": "controlplane"},
		managedLabels(current, map[string]string{"role": "worker", "zone": "a"}),
		"reports the current value of the managed keys still present, and nothing else")

	assert.Empty(t, managedLabels(current, map[string]string{"zone": "a"}))
}

func TestAdoptableLabels(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		map[string]string{"role": "worker"},
		adoptableLabels(map[string]string{
			"role":                    "worker",
			"omni.sidero.dev/cluster": "reserved",
			"control\x01character":    "invalid",
		}),
		"adopts only the labels the configuration could hold")
}
