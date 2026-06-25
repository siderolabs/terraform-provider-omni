// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package main implements the Terraform provider for Omni.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/siderolabs/terraform-provider-omni/pkg/omni"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), omni.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/siderolabs/omni",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
