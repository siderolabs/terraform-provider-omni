#!/usr/bin/env bash
#
# End-to-end test with real machines, on Proxmox.
#
# Brings up a throwaway Omni instance and a containerized Proxmox VE node, puts a stock Talos
# `nocloud` ISO on the node, and runs the machine-backed acceptance test. The test creates the VMs
# itself, through examples/modules/omni-proxmox-cluster/modules/machine: each VM boots the ISO with
# Omni's join config as its cloud-init user data, `omni_machine_wait` resolves its UUID from a label
# the join config carries, and `omni_cluster_wait` waits for the cluster. Tears everything down on
# exit.
#
# The Proxmox setup follows the integration test of the Omni Proxmox infra provider
# (siderolabs/omni-infra-provider-proxmox, hack/test/integration.sh).
#
# Requirements: KVM, docker, curl, jq, envsubst. See .github/workflows/acceptance-tests-proxmox.yaml.
#
# Usage: hack/test/run-proxmox.sh [extra go test args...]
#   OMNI_VERSION            Omni image tag to test against (default: latest).
#   TALOS_ISO_VERSION       Talos version of the boot ISO (default: 1.14.5). Omni installs the
#                           cluster's own version once a machine is allocated.
#   PROXMOX_SUBNET_PREFIX   First three octets of the Proxmox network (default: 10.0.99).
#   PROXMOX_NETWORK_MTU     MTU of the Proxmox network and the VMs (default: 1450, the CI runner's).
#                           Lower it, and OMNI_TEST_NETWORK_MTU, on a host whose uplink is smaller.
#   KEEP_ENVIRONMENT=true   Leave Omni and Proxmox running after the test, for debugging.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

# See run.sh: reusing one Terraform binary avoids a "text file busy" race between parallel tests.
if [[ -z "${TF_ACC_TERRAFORM_PATH:-}" ]] && command -v terraform >/dev/null 2>&1; then
  TF_ACC_TERRAFORM_PATH="$(command -v terraform)"
  export TF_ACC_TERRAFORM_PATH
fi

if [[ "${CI:-false}" == "true" ]] && ! docker compose version >/dev/null 2>&1; then
  COMPOSE_VERSION=v2.32.4
  install -d /usr/local/lib/docker/cli-plugins
  curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-$(uname -m)" \
    -o /usr/local/lib/docker/cli-plugins/docker-compose
  chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
fi

for bin in docker curl jq envsubst; do
  if ! command -v "${bin}" >/dev/null 2>&1; then
    echo "required binary not found: ${bin}" >&2
    exit 1
  fi
done

if [[ ! -e /dev/kvm ]]; then
  echo "/dev/kvm is not available; this test requires hardware virtualization" >&2
  exit 1
fi

PROJECT=omni-tf-acc-proxmox
COMPOSE=(docker compose -p "${PROJECT}" -f hack/test/docker-compose.yaml -f hack/test/docker-compose.proxmox.yaml)

export OMNI_VERSION="${OMNI_VERSION:-latest}"
export OMNI_HOST_PORT="${OMNI_HOST_PORT:-8099}"
export S3_HOST_PORT="${S3_HOST_PORT:-8333}"
export PROXMOX_SUBNET_PREFIX="${PROXMOX_SUBNET_PREFIX:-10.0.99}"
export PROXMOX_NETWORK_MTU="${PROXMOX_NETWORK_MTU:-1450}"
export PROXMOX_VE_PASSWORD="${PROXMOX_VE_PASSWORD:-testonly}"

TALOS_ISO_VERSION="${TALOS_ISO_VERSION:-1.14.5}"
FACTORY_URL="${FACTORY_URL:-https://factory.talos.dev}"
ISO_CACHE_DIR="${ISO_CACHE_DIR:-${REPO_ROOT}/_out/iso}"

PVE_NODE=pve-1
PVE_IP="${PROXMOX_SUBNET_PREFIX}.1"
GATEWAY_IP="${PROXMOX_SUBNET_PREFIX}.99"
# Omni's address on the Proxmox network (see docker-compose.proxmox.yaml), which is what the join
# config tells the VMs to connect to.
export OMNI_SIDEROLINK_ADVERTISED_HOST="${PROXMOX_SUBNET_PREFIX}.98"

cleanup() {
  "${COMPOSE[@]}" logs --no-color > /tmp/omni-tf-acc-proxmox.log 2>&1 || true

  if [[ "${KEEP_ENVIRONMENT:-false}" == "true" ]]; then
    echo "==> KEEP_ENVIRONMENT is set, leaving Omni at ${OMNI_ENDPOINT:-?} and Proxmox at https://${PVE_IP}:8006 running"

    return
  fi

  # The VMs run inside the Proxmox container, so removing it takes them down as well.
  "${COMPOSE[@]}" down -t 5 -v --remove-orphans || true
}
trap cleanup EXIT INT TERM

# --- Talos ISO -------------------------------------------------------------------------------------
#
# A stock nocloud image plus the QEMU guest agent: nocloud reads the join config from the cloud-init
# drive, and the module gives the VMs a guest agent channel, which the extension answers on.

echo "==> Resolving the Talos ${TALOS_ISO_VERSION} nocloud ISO"
SCHEMATIC_ID="$(curl -fsSL -X POST --data-binary @- "${FACTORY_URL}/schematics" <<'EOF' | jq -r '.id'
customization:
  systemExtensions:
    officialExtensions:
      - siderolabs/qemu-guest-agent
EOF
)"

if [[ -z "${SCHEMATIC_ID}" || "${SCHEMATIC_ID}" == "null" ]]; then
  echo "failed to create the image factory schematic" >&2
  exit 1
fi

ISO_NAME="talos-${TALOS_ISO_VERSION}-${SCHEMATIC_ID:0:12}-nocloud-amd64.iso"
ISO_PATH="${ISO_CACHE_DIR}/${ISO_NAME}"

if [[ ! -s "${ISO_PATH}" ]]; then
  mkdir -p "${ISO_CACHE_DIR}"
  curl -fsSL -o "${ISO_PATH}.tmp" "${FACTORY_URL}/image/${SCHEMATIC_ID}/v${TALOS_ISO_VERSION}/nocloud-amd64.iso"
  mv "${ISO_PATH}.tmp" "${ISO_PATH}"
fi

# --- Omni and Proxmox ------------------------------------------------------------------------------

echo "==> Starting Omni, mock OIDC, S3 and Proxmox"
envsubst '${OMNI_SIDEROLINK_ADVERTISED_HOST}' < hack/test/omni-config.yaml.tmpl > hack/test/omni-config.yaml
"${COMPOSE[@]}" down -t 5 -v --remove-orphans >/dev/null 2>&1 || true
# Pull Omni even when an image is present: a stale `latest` from an earlier run would test an older
# Omni than the one asked for, and one predating machine labels in join tokens cannot pass at all.
"${COMPOSE[@]}" pull omni
"${COMPOSE[@]}" up -d

OMNI_CID="$("${COMPOSE[@]}" ps -q omni)"
PVE_CID="$("${COMPOSE[@]}" ps -q "${PVE_NODE}")"

if [[ -z "${OMNI_CID}" || -z "${PVE_CID}" ]]; then
  echo "failed to determine the Omni or Proxmox container id" >&2
  exit 1
fi

pve() {
  docker exec -i "${PVE_CID}" "$@"
}

wait_pve_ready() {
  local deadline=$(( $(date +%s) + 300 ))

  until pve pvesh get /version >/dev/null 2>&1; do
    if [[ $(date +%s) -gt ${deadline} ]]; then
      echo "Proxmox did not become ready in time" >&2
      docker logs "${PVE_CID}" 2>&1 | tail -50 >&2 || true
      exit 1
    fi
    sleep 3
  done
}

echo "==> Configuring Proxmox"
wait_pve_ready

# CI runners disable io_uring host-wide (kernel.io_uring_disabled=2), and Proxmox defaults every drive
# without an explicit aio setting (the cloud-init drive, the ISO) to io_uring, so QEMU would fail to
# start. Make qemu-server fall back to threads or native for all storages.
DRIVE_PM=/usr/share/perl5/PVE/QemuServer/Drive.pm
pve grep -q '^sub storage_allows_io_uring_default {$' "${DRIVE_PM}"
pve sed -i 's/^sub storage_allows_io_uring_default {$/&\n    return;/' "${DRIVE_PM}"
pve systemctl restart pvedaemon pveproxy
wait_pve_ready

# VM disks need "images" and the cloud-init user data needs "snippets", neither of which a fresh
# node enables on `local`.
pve pvesm set local --content iso,vztmpl,backup,images,snippets,rootdir

# The image ships vmbr1 as a NAT'd island and vmbr2 as an empty bridge. Bridge vmbr2 onto eth0 so the
# VMs sit directly on the docker network next to Omni, and drop vmbr1, whose hooks load iptables
# modules a CI host kernel may not have.
pve pvesh delete "/nodes/${PVE_NODE}/network/vmbr1" || true
pve rm -f /etc/dnsmasq.d/vmbr1.conf
pve pvesh set "/nodes/${PVE_NODE}/network/vmbr2" \
  --type bridge \
  --bridge_ports eth0 \
  --cidr "${PVE_IP}/24" \
  --gateway "${GATEWAY_IP}" \
  --mtu "${PROXMOX_NETWORK_MTU}"
pve pvesh set "/nodes/${PVE_NODE}/network"

# Moving eth0's address onto vmbr2 briefly takes the API down.
sleep 3
wait_pve_ready

# DHCP for the VMs: the docker gateway as the route out, and the network's MTU.
pve tee /etc/dnsmasq.d/vmbr2.conf >/dev/null <<EOF
# Managed by hack/test/run-proxmox.sh
interface=vmbr2

dhcp-range=set:vmbr2,${PROXMOX_SUBNET_PREFIX}.10,${PROXMOX_SUBNET_PREFIX}.89,255.255.255.0,12h
dhcp-option=tag:vmbr2,3,${GATEWAY_IP}
dhcp-option=tag:vmbr2,26,${PROXMOX_NETWORK_MTU}
EOF
pve systemctl restart dnsmasq

echo "==> Uploading the Talos ISO"
docker cp "${ISO_PATH}" "${PVE_CID}:/var/lib/vz/template/iso/${ISO_NAME}"

# --- Omni service account --------------------------------------------------------------------------

echo "==> Waiting for the initial service-account key"
deadline=$(( $(date +%s) + 180 ))
KEY=""
until KEY="$(docker run --rm --volumes-from "${OMNI_CID}" alpine sh -c '[ -s /_out/key ] && cat /_out/key' 2>/dev/null)" && [[ -n "${KEY}" ]]; do
  if [[ $(date +%s) -gt ${deadline} ]]; then
    echo "Omni did not write the service-account key in time" >&2
    "${COMPOSE[@]}" logs --no-color omni | tail -50 >&2 || true
    exit 1
  fi
  sleep 2
done

export OMNI_ENDPOINT="https://127.0.0.1:${OMNI_HOST_PORT}"
export OMNI_SERVICE_ACCOUNT_KEY="${KEY}"

# Read by the test (see internal/integration/provider_test.go). The password reaches the Proxmox
# provider as PROXMOX_VE_PASSWORD, which it reads itself.
export PROXMOX_VE_ENDPOINT="https://${PVE_IP}:8006/"
export PROXMOX_NODE="${PVE_NODE}"
export PROXMOX_SSH_ADDRESS="${PVE_IP}"
export PROXMOX_SSH_PORT=22
export PROXMOX_BRIDGE=vmbr2
export PROXMOX_TALOS_ISO="local:iso/${ISO_NAME}"

echo "==> Running the machine-backed acceptance test against ${OMNI_ENDPOINT} and ${PROXMOX_VE_ENDPOINT}"
go test -tags proxmox ./internal/integration/... -run TestAccOmniClusterWithMachines -v -count=1 -timeout 90m "$@"
