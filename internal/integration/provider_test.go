// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

//go:build proxmox

package integration_test

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"

	omniclient "github.com/siderolabs/omni/client/pkg/client"
)

// proxmoxConfig is where the test creates its VMs, read from the environment hack/test/run-proxmox.sh
// sets up. Any Proxmox VE host works, given the same settings.
type proxmoxConfig struct {
	// Endpoint is the Proxmox API URL (PROXMOX_VE_ENDPOINT). The password is not here: the Proxmox
	// provider reads PROXMOX_VE_PASSWORD itself, which keeps it out of the rendered configuration.
	Endpoint string
	// Node is the node to create the VMs on (PROXMOX_NODE, default pve-1).
	Node string
	// SSHAddress and SSHPort reach the node over SSH, which is how cloud-init snippets are uploaded
	// (PROXMOX_SSH_ADDRESS, default the endpoint's host; PROXMOX_SSH_PORT, default 22).
	SSHAddress string
	// TalosISO is a Talos nocloud ISO on the node, as a volume ID (PROXMOX_TALOS_ISO).
	TalosISO string
	// Bridge is the bridge the VMs are attached to, which needs DHCP and a route to Omni
	// (PROXMOX_BRIDGE, default vmbr0).
	Bridge  string
	SSHPort int
}

var proxmox proxmoxConfig

// TestMain gates the whole integration suite on a configured Omni instance and Proxmox host: every
// test here talks to both, so when either is not configured (e.g. a plain `go test ./...`) the
// package is skipped instead of failing.
func TestMain(m *testing.M) {
	for _, name := range []string{"OMNI_ENDPOINT", "OMNI_SERVICE_ACCOUNT_KEY", "PROXMOX_VE_ENDPOINT", "PROXMOX_VE_PASSWORD", "PROXMOX_TALOS_ISO"} {
		if os.Getenv(name) == "" {
			fmt.Fprintf(os.Stderr, "skipping integration tests: %s must be set\n", name)

			return
		}
	}

	var err error

	if proxmox, err = proxmoxConfigFromEnv(); err != nil {
		fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}

	// The configurations use `source = "siderolabs/omni"`, as the module does, so the in-process
	// provider has to be registered under that address rather than the default hashicorp/omni.
	if err = os.Setenv("TF_ACC_PROVIDER_NAMESPACE", "siderolabs"); err != nil {
		fmt.Fprintln(os.Stderr, err)

		os.Exit(1)
	}

	os.Exit(m.Run())
}

func proxmoxConfigFromEnv() (proxmoxConfig, error) {
	config := proxmoxConfig{
		Endpoint:   os.Getenv("PROXMOX_VE_ENDPOINT"),
		Node:       envOr("PROXMOX_NODE", "pve-1"),
		SSHAddress: os.Getenv("PROXMOX_SSH_ADDRESS"),
		SSHPort:    22,
		TalosISO:   os.Getenv("PROXMOX_TALOS_ISO"),
		Bridge:     envOr("PROXMOX_BRIDGE", "vmbr0"),
	}

	if port := os.Getenv("PROXMOX_SSH_PORT"); port != "" {
		var err error

		if config.SSHPort, err = strconv.Atoi(port); err != nil {
			return config, fmt.Errorf("PROXMOX_SSH_PORT: %w", err)
		}
	}

	if config.SSHAddress == "" {
		host, err := hostOf(config.Endpoint)
		if err != nil {
			return config, fmt.Errorf("PROXMOX_VE_ENDPOINT: %w", err)
		}

		config.SSHAddress = host
	}

	return config, nil
}

// hostOf returns the host of a URL, without the port.
func hostOf(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	if parsed.Hostname() == "" {
		return "", fmt.Errorf("%q has no host", rawURL)
	}

	return parsed.Hostname(), nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}

// newTestClient builds an Omni client from the acceptance-test environment.
func newTestClient() (*omniclient.Client, error) {
	return omniclient.New(
		os.Getenv("OMNI_ENDPOINT"),
		omniclient.WithServiceAccount(os.Getenv("OMNI_SERVICE_ACCOUNT_KEY")),
		omniclient.WithInsecureSkipTLSVerify(true),
	)
}
