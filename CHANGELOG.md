## [Omni Terraform Provider 0.1.0](https://github.com/siderolabs/terraform-provider-omni/releases/tag/v0.1.0) (2026-11-27)

Welcome to the v0.1.0 release of Omni Terraform Provider!



Please try out the release binaries and report any issues at
https://github.com/siderolabs/terraform-provider-omni/issues.

### Installation Media URLs

The new `omni_installation_media` data source resolves an `omni_installation_media_preset` into a downloadable URL, so the media can be passed to a hypervisor, a PXE server or a plain fetch.


### Join Tokens

The new `omni_join_token` and `omni_default_join_token` resources manage join tokens and the instance default token.
The new `omni_machine_join_config` data source returns the partial Talos machine configuration and kernel arguments a machine needs to join Omni from a stock Talos image.


### Machine gRPC Tunnel

The new `omni_machine_grpc_tunnel` resource sets whether a machine tunnels its SideroLink traffic through gRPC, like `omnictl configure machine --siderolink-connection`.


### Machine Labels

The new `omni_machine_labels` resource sets user labels on a machine. It manages only the keys it is given, so labels set by Omni or in the UI are left untouched.


### Machine and Cluster Wait

The new `omni_machine_wait` resource waits for a machine to register with Omni and records its UUID, selecting it by labels or by machine ID.
The new `omni_cluster_wait` resource waits for a cluster to become ready, including the configured Talos and Kubernetes versions after an upgrade.


### Contributors

* Artem Chernyshev

### Changes
<details><summary>5 commits</summary>
<p>

* [`bb4da28`](https://github.com/siderolabs/terraform-provider-omni/commit/bb4da2890c9c3ee1872caf9b4830f1023900c440) release(v0.1.0): prepare release
* [`75f1413`](https://github.com/siderolabs/terraform-provider-omni/commit/75f141302a654c185dc752ddfe0b9aad91a06006) feat: support machine labels and gRPC tunnel mode resources (#27)
* [`a8b3e16`](https://github.com/siderolabs/terraform-provider-omni/commit/a8b3e16a87a56dd52cd209cf8bda58c5cc0b7897) feat: support machine and cluster wait operations (#26)
* [`cd5ebce`](https://github.com/siderolabs/terraform-provider-omni/commit/cd5ebce352e3b71de6c5da8d75b9f883dbfad9f1) feat: support join token management (#25)
* [`e173a0d`](https://github.com/siderolabs/terraform-provider-omni/commit/e173a0d450b23b92ff02a4b8e76121efb50ad6e0) feat: support reading installation media URLs from Omni
</p>
</details>

### Dependency Changes

* **github.com/cosi-project/runtime**                           v1.16.3 -> v1.17.1
* **github.com/hashicorp/terraform-plugin-framework-timeouts**  v0.7.0 **_new_**
* **github.com/hashicorp/terraform-plugin-log**                 v0.11.0 **_new_**
* **github.com/siderolabs/gen**                                 v0.8.7 -> v0.8.8
* **github.com/siderolabs/go-api-signature**                    v0.3.13 -> v0.4.0
* **github.com/siderolabs/omni/client**                         v1.12.2 -> v1.13.3

Previous release can be found at [v0.1.0-beta.0](https://github.com/siderolabs/terraform-provider-omni/releases/tag/v0.1.0-beta.0)

## [Omni Terraform Provider 0.1.0-beta.0](https://github.com/siderolabs/terraform-provider-omni/releases/tag/v0.1.0-beta.0) (2026-09-28)

Welcome to the v0.1.0-beta.0 release of Omni Terraform Provider!  
*This is a pre-release of Omni Terraform Provider*



Please try out the release binaries and report any issues at
https://github.com/siderolabs/terraform-provider-omni/issues.

### etcd Backup S3 Configuration

The new `omni_etcd_backup_s3_config` resource manages the instance-wide S3 target for etcd backups, complementing the per-cluster `backup_interval` on `omni_cluster`.


### Infrastructure Providers

The new `omni_infra_provider` resource registers an infrastructure provider in Omni together with its service account and exposes the generated key for the provider to authenticate with.


### Installation Media Presets

The new `omni_installation_media_preset` resource manages installation media presets from Terraform.


### Machine Classes

The new `omni_machine_class` resource manages machine classes that either match existing machines by labels or auto-provision them through an infrastructure provider.


### Machine Install Disk

The new `omni_machine_install_disk` resource sets a machine's Talos install disk, either by device path or by a CEL disk selector expression.


### Service Accounts

The new `omni_service_account` resource and data source manage Omni service accounts from Terraform.


### Contributors

* Mateusz Urbanek
* Artem Chernyshev
* Justin Garrison
* Nate O'Farrell

### Changes
<details><summary>7 commits</summary>
<p>

* [`ce78e87`](https://github.com/siderolabs/terraform-provider-omni/commit/ce78e87bcac2962dd0eb914e72615dd794d76fcb) feat: add omni_machine_install_disk resource
* [`8ea6816`](https://github.com/siderolabs/terraform-provider-omni/commit/8ea68163be26bf5499665bf06afb32698eb1bab3) feat: add infra_provider resource
* [`686b15d`](https://github.com/siderolabs/terraform-provider-omni/commit/686b15d59a758e970ccda693486c0c54ebe0571c) fix: keep configured name on named machine sets
* [`f6973bf`](https://github.com/siderolabs/terraform-provider-omni/commit/f6973bf9c99df21763402ca9479d7dda79b98bda) feat: add service account support
* [`f40ebb5`](https://github.com/siderolabs/terraform-provider-omni/commit/f40ebb583d119b64f21f27091e6f17fdccfd2766) feat: add omni_machine_class resource
* [`395fd29`](https://github.com/siderolabs/terraform-provider-omni/commit/395fd299609566b52d95992373f5cbd4b15ee3f6) feat: implement installation media preset support
* [`1269e66`](https://github.com/siderolabs/terraform-provider-omni/commit/1269e661638a412f09c6bd6874ca3f901d7f434c) feat: add omni_etcd_backup_s3_config resource
</p>
</details>

### Dependency Changes

* **github.com/cosi-project/runtime**    v1.16.2 -> v1.16.3
* **github.com/google/uuid**             v1.6.0 **_new_**
* **github.com/siderolabs/gen**          v0.8.6 -> v0.8.7
* **github.com/siderolabs/omni/client**  49c8e725f6a1 -> v1.12.2
* **github.com/stretchr/testify**        v1.12.1 **_new_**
* **google.golang.org/protobuf**         f2248ac996af -> v1.36.12

Previous release can be found at [v0.1.0-alpha.3](https://github.com/siderolabs/terraform-provider-omni/releases/tag/v0.1.0-alpha.3)

## [Omni Terraform Provider 0.1.0-alpha.0](https://github.com/siderolabs/terraform-provider-omni/releases/tag/v0.1.0-alpha.0) (2026-07-08)

Welcome to the v0.1.0-alpha.0 release of Omni Terraform Provider!



Please try out the release binaries and report any issues at
https://github.com/siderolabs/terraform-provider-omni/issues.

### Contributors

* Artem Chernyshev

### Changes
<details><summary>4 commits</summary>
<p>

* [`4d0dc31`](https://github.com/siderolabs/terraform-provider-omni/commit/4d0dc31a2b5a777ee3cd508e17ff9c0065c4b1e7) feat: add cluster management support
* [`e66ef24`](https://github.com/siderolabs/terraform-provider-omni/commit/e66ef2492c5118de0d9c1f79415d58726270d998) feat: initial version of the provider that supports user management
* [`e3fb9f2`](https://github.com/siderolabs/terraform-provider-omni/commit/e3fb9f2674ae1d70fae8812379577c84aaed3b1e) test: add GHA
* [`3fcdd05`](https://github.com/siderolabs/terraform-provider-omni/commit/3fcdd0580d33ca812b5f8eebdb3fbfedd11a92f1) first commit
</p>
</details>

### Dependency Changes

This release has no dependency changes

