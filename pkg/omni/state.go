// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"context"

	"github.com/cosi-project/runtime/pkg/resource"
	cosistate "github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
)

// Terraform is the value set on the omni.ResourceManagedByGitopsTools annotation to indicate that
// a resource is managed by this Terraform provider.
const Terraform = "Terraform"

// managedState wraps a COSI state and tags every resource it creates or updates with the
// ResourceManagedByGitopsTools annotation, so that Omni knows the resource is managed by the
// Terraform provider.
type managedState struct {
	cosistate.State
}

// newManagedState wraps the given state so that resources it creates or updates are marked as
// managed by the Terraform provider.
func newManagedState(state cosistate.State) cosistate.State {
	return &managedState{State: state}
}

// markManagedByTerraform sets the ResourceManagedByGitopsTools annotation on the resource.
func markManagedByTerraform(res resource.Resource) {
	res.Metadata().Annotations().Set(omni.ResourceManagedByGitopsTools, Terraform)
}

// Create implements state.State.
func (s *managedState) Create(ctx context.Context, res resource.Resource, opts ...cosistate.CreateOption) error {
	markManagedByTerraform(res)

	return s.State.Create(ctx, res, opts...)
}

// Update implements state.State.
func (s *managedState) Update(ctx context.Context, res resource.Resource, opts ...cosistate.UpdateOption) error {
	markManagedByTerraform(res)

	return s.State.Update(ctx, res, opts...)
}

// UpdateWithConflicts implements state.State.
func (s *managedState) UpdateWithConflicts(
	ctx context.Context, ptr resource.Pointer, fn cosistate.UpdaterFunc, opts ...cosistate.UpdateOption,
) (resource.Resource, error) {
	return s.State.UpdateWithConflicts(ctx, ptr, func(res resource.Resource) error {
		if err := fn(res); err != nil {
			return err
		}

		markManagedByTerraform(res)

		return nil
	}, opts...)
}
