# Terraform Provider for Omni

A [Terraform](https://www.terraform.io) / [OpenTofu](https://opentofu.org) provider for
[Siderolabs Omni](https://github.com/siderolabs/omni).

Omni exposes its API as [COSI](https://github.com/cosi-project/runtime) resources. This provider
talks to an Omni instance using the official [Omni Go client](https://github.com/siderolabs/omni/tree/main/client)
and lets you manage Omni objects declaratively. It is modeled on the
[terraform-provider-talos](https://github.com/siderolabs/terraform-provider-talos) provider and
uses the [terraform-plugin-framework](https://github.com/hashicorp/terraform-plugin-framework).

## Status

Early skeleton. The first supported object is users (`omni_user`).

## Provider configuration

```hcl
terraform {
  required_providers {
    omni = {
      source = "siderolabs/omni"
    }
  }
}

provider "omni" {
  endpoint = "https://instance.omni.siderolabs.io"
  # service_account_key = "..."  # prefer OMNI_SERVICE_ACCOUNT_KEY env var
}
```

| Argument | Env var | Description |
| --- | --- | --- |
| `endpoint` | `OMNI_ENDPOINT` | Omni API endpoint. |
| `service_account_key` | `OMNI_SERVICE_ACCOUNT_KEY` | Base64-encoded Omni service account key. |
| `insecure_skip_tls_verify` | – | Skip TLS verification (development only). |

Create a service account key with `omnictl serviceaccount create`.

## Resources

### `omni_user`

Manages an Omni user and its associated identity.

```hcl
resource "omni_user" "alice" {
  email = "alice@example.com"
  role  = "Operator"
}
```

- `email` (required, forces replacement) — the user identity, keyed by email.
- `role` (required) — one of `None`, `Reader`, `Operator`, `Admin`, `InfraProvider`.
- `id` (computed) — the generated user UUID.

Import an existing user by email:

```sh
terraform import omni_user.alice alice@example.com
```

## Data sources

### `omni_user`

Looks up an existing Omni user by email, exposing its `id` and `role`.

## Development

```sh
make build             # build the provider binary
make test              # unit tests
make vet               # go vet
make docs              # regenerate docs/ via tfplugindocs
make test-integration  # acceptance tests against a throwaway Omni (docker compose)
```

The repo expects the Omni client module to be available; for local development a
`replace github.com/siderolabs/omni/client => ../omni/client` directive points at a checkout.

### Acceptance tests

`make test-integration` (i.e. `hack/test/run.sh`) brings up a throwaway Omni instance and a mock
OIDC server via `hack/test/docker-compose.yaml`, extracts the bootstrapped service-account key,
and runs the `TestAcc*` tests against it (`TF_ACC=1`). Omni uses a checked-in throwaway PGP key
(`file://` private-key-source, no Vault) and self-signed certs under `hack/test/certs/`; the tests
connect with `insecure_skip_tls_verify = true`. No real Auth0 tenant is required — the
service-account key is PGP-signed, so the OIDC backend is never contacted.

If port `8099` is already in use locally, override it:

```sh
OMNI_HOST_PORT=18099 make test-integration
```

The acceptance tests also run in CI via `.github/workflows/acceptance-tests.yaml`, which checks out
`siderolabs/omni` as a sibling directory so the `../omni/client` replace resolves.

## License

[MPL-2.0](LICENSE)
