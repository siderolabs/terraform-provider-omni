// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build qemu

package integration_test

import (
	"fmt"
	"os"
	"testing"

	omniclient "github.com/siderolabs/omni/client/pkg/client"
)

// TestMain gates the whole integration suite on a configured Omni instance: every test here talks to
// a live Omni, so when the connection environment is absent (e.g. a plain `go test ./...`) the
// package is skipped instead of failing.
func TestMain(m *testing.M) {
	if os.Getenv("OMNI_ENDPOINT") == "" || os.Getenv("OMNI_SERVICE_ACCOUNT_KEY") == "" {
		fmt.Fprintln(os.Stderr, "skipping integration tests: OMNI_ENDPOINT and OMNI_SERVICE_ACCOUNT_KEY must be set")

		return
	}

	os.Exit(m.Run())
}

// newTestClient builds an Omni client from the acceptance-test environment.
func newTestClient() (*omniclient.Client, error) {
	return omniclient.New(
		os.Getenv("OMNI_ENDPOINT"),
		omniclient.WithServiceAccount(os.Getenv("OMNI_SERVICE_ACCOUNT_KEY")),
		omniclient.WithInsecureSkipTLSVerify(true),
	)
}
