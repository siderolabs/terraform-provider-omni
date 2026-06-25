// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package omni_test

import (
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// testAccProtoV6ProviderFactories wires the in-process provider for acceptance tests.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"omni": providerserver.NewProtocol6WithError(omni.New("test")()),
}

// testAccPreCheck verifies the environment is configured to reach an Omni instance.
func testAccPreCheck(t *testing.T) {
	t.Helper()

	if os.Getenv("OMNI_ENDPOINT") == "" {
		t.Fatal("OMNI_ENDPOINT must be set for acceptance tests")
	}

	if os.Getenv("OMNI_SERVICE_ACCOUNT_KEY") == "" {
		t.Fatal("OMNI_SERVICE_ACCOUNT_KEY must be set for acceptance tests")
	}
}
