# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/.

NAME := terraform-provider-omni
VERSION ?= dev
ARTIFACTS ?= _out

.PHONY: build
build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(NAME) .

.PHONY: install
install:
	go install -ldflags "-X main.version=$(VERSION)" .

.PHONY: test
test:
	go test ./...

.PHONY: testacc
testacc:
	TF_ACC=1 go test ./... -v -timeout 120m

# Brings up a throwaway Omni instance via docker compose and runs the acceptance tests against it.
.PHONY: test-integration
test-integration:
	./hack/test/run.sh

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint:
	golangci-lint run

.PHONY: go-vulncheck
go-vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Generates SPDX and CycloneDX SBOMs into $(ARTIFACTS).
.PHONY: sbom
sbom:
	@mkdir -p $(ARTIFACTS)
	SYFT_FORMAT_PRETTY=1 SYFT_FORMAT_SPDX_JSON_DETERMINISTIC_UUID=1 go run github.com/anchore/syft/cmd/syft@latest dir:. -o spdx-json > $(ARTIFACTS)/sbom.spdx.json
	SYFT_FORMAT_PRETTY=1 SYFT_FORMAT_SPDX_JSON_DETERMINISTIC_UUID=1 go run github.com/anchore/syft/cmd/syft@latest dir:. -o cyclonedx-json > $(ARTIFACTS)/sbom.cyclonedx.json

.PHONY: docs
docs:
	go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate --provider-name omni

.PHONY: tidy
tidy:
	go mod tidy

# Regenerates derived files (go.mod/go.sum and docs) and fails if the working tree is dirty.
.PHONY: check-dirty
check-dirty: tidy docs
	@if ! git diff --quiet; then \
		echo "Working tree is dirty after 'make tidy docs'. Run them locally and commit the changes:"; \
		git status --short; \
		git --no-pager diff; \
		exit 1; \
	fi
