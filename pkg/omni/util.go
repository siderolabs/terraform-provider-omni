// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// providerDataFromResource extracts the shared providerData set by the provider's Configure method.
//
// It returns nil (without adding a diagnostic) when data is nil, which happens during the
// framework's initial validation pass before the provider is configured.
func providerDataFromResource(in any, diags *diag.Diagnostics) *providerData {
	if in == nil {
		return nil
	}

	data, ok := in.(*providerData)
	if !ok {
		diags.AddError(
			"Unexpected provider data type",
			fmt.Sprintf("Expected *providerData, got %T. This is a bug in the provider.", in),
		)

		return nil
	}

	return data
}

// errToDiag appends a Go error to the diagnostics as an error with the given summary.
func errToDiag(diags *diag.Diagnostics, summary string, err error) {
	diags.AddError(summary, err.Error())
}
