#!/usr/bin/env bash
#
# End-to-end test with real machines.
#
# Brings up a throwaway Omni instance, provisions QEMU VMs that boot a maintenance image (an Image
# Factory ISO carrying the SideroLink join kernel args for this Omni), waits for them to register,
# then runs the machine-backed acceptance test (cluster bootstrap + scale up/down). Tears everything
# down on exit.
#
# Mirrors the approach used by Omni's own QEMU integration tests (hack/test/common.sh): a kernel-args
# schematic is POSTed to the Image Factory and the resulting ISO is booted via `talosctl cluster
# create ... --skip-injecting-config`, so the machines come up in maintenance mode and join Omni over
# SideroLink. No omnictl is required.
#
# Requirements: KVM, qemu (assumed present in CI), crane, jq, curl, docker
# (the QEMU provisioner manages bridges/taps/iptables). See
# .github/workflows/acceptance-tests-qemu.yaml.
#
# Usage: hack/test/run-qemu.sh [extra go test args...]
#   OMNI_VERSION             Omni image tag to test against (default: latest).
#   OMNI_QEMU_MACHINE_COUNT  Number of VMs to provision (default: 4 — 3 control planes + 1 worker).
#   QEMU_TALOS_VERSION       Talos version for the maintenance boot media (default: 1.13.5).
#   QEMU_MEMORY / QEMU_CPUS  Per-VM memory (MiB) and vCPUs (default: 3072 / 3).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

if [[ "${CI:-false}" == "true" ]] && ! docker compose version >/dev/null 2>&1; then
  COMPOSE_VERSION=v2.32.4
  install -d /usr/local/lib/docker/cli-plugins
  curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-$(uname -m)" \
    -o /usr/local/lib/docker/cli-plugins/docker-compose
  chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
fi

PROJECT=omni-tf-acc-qemu
COMPOSE_FILE=hack/test/docker-compose.yaml
COMPOSE=(docker compose -p "${PROJECT}" -f "${COMPOSE_FILE}")

CLUSTER_NAME=tf-acc-qemu
QEMU_CIDR="172.20.0.0/24"
# talosctl's QEMU provisioner assigns the first address of the CIDR to the host bridge; that gateway
# is both the SideroLink WireGuard endpoint advertised to the VMs and the address baked into the
# boot media's kernel args.
WIREGUARD_IP="172.20.0.1"

MACHINE_COUNT="${OMNI_QEMU_MACHINE_COUNT:-4}"
QEMU_TALOS_VERSION="${QEMU_TALOS_VERSION:-1.13.5}"
QEMU_MEMORY="${QEMU_MEMORY:-3072}"
QEMU_CPUS="${QEMU_CPUS:-3}"
FACTORY_API_URL="${FACTORY_API_URL:-https://factory.talos.dev}"

ARTIFACTS="$(mktemp -d)"
TALOSCTL="${ARTIFACTS}/talosctl"
OMNICTL="${ARTIFACTS}/omnictl"

echo "==> Downloading omnictl"

curl -Lo ${OMNICTL} $(curl https://api.github.com/repos/siderolabs/omni/releases/latest  |  jq -r '.assets[] | select(.name | contains ("omnictl-linux-amd64")) | .browser_download_url')
chmod +x ${OMNICTL}

export OMNI_HOST_PORT="${OMNI_HOST_PORT:-8099}"
# Advertise SideroLink on the QEMU bridge gateway so the VMs can reach Omni (the machine-less suite
# uses loopback instead).
export OMNI_SIDEROLINK_ADVERTISED_HOST="${WIREGUARD_IP}"

for bin in crane jq curl docker envsubst; do
  if ! command -v "${bin}" >/dev/null 2>&1; then
    echo "required binary not found: ${bin}" >&2
    exit 1
  fi
done

if [[ ! -e /dev/kvm ]]; then
  echo "/dev/kvm is not available; this test requires hardware virtualization" >&2
  exit 1
fi

cleanup() {
  echo "==> Tearing down QEMU machines"
  "${TALOSCTL}" cluster destroy --provisioner=qemu --name="${CLUSTER_NAME}" >/dev/null 2>&1 || true

  "${COMPOSE[@]}" logs --no-color > /tmp/omni-tf-acc-qemu.log 2>&1 || true
  "${COMPOSE[@]}" down -t 5 -v --remove-orphans || true

  rm -rf "${ARTIFACTS}"
}
trap cleanup EXIT INT TERM

echo "==> Downloading talosctl"
crane export ghcr.io/siderolabs/talosctl:latest - | tar x -C "${ARTIFACTS}" talosctl
chmod +x "${TALOSCTL}"

echo "==> Starting Omni and mock OIDC"
# Render the Omni config from its template (see hack/test/omni-config.yaml.tmpl).
envsubst '${OMNI_SIDEROLINK_ADVERTISED_HOST}' < hack/test/omni-config.yaml.tmpl > hack/test/omni-config.yaml
"${COMPOSE[@]}" down -t 5 -v --remove-orphans >/dev/null 2>&1 || true
"${COMPOSE[@]}" up -d

OMNI_CID="$("${COMPOSE[@]}" ps -q omni)"
if [[ -z "${OMNI_CID}" ]]; then
  echo "failed to determine Omni container id" >&2
  exit 1
fi

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

JOIN_TOKEN=$(${OMNICTL} get defaultjointoken --insecure-skip-tls-verify -ojson | jq -r '.spec.tokenid')

echo "==> Creating Image Factory schematic with SideroLink kernel args"
# The events sink and kernel-log endpoints are Omni's fixed SideroLink WireGuard address, reached
# over the tunnel once the machine connects to siderolink.api.
SCHEMATIC=$(cat <<EOF
customization:
  extraKernelArgs:
    - siderolink.api=grpc://${WIREGUARD_IP}:8090?jointoken=${JOIN_TOKEN}
    - talos.events.sink=[fdae:41e4:649b:9303::1]:8091
    - talos.logging.kernel=tcp://[fdae:41e4:649b:9303::1]:8092
    - console=tty0
    - console=ttyS0
EOF
)

SCHEMATIC_ID="$(curl -fsSL -X POST --data-binary "${SCHEMATIC}" "${FACTORY_API_URL}/schematics" | jq -r '.id')"
if [[ -z "${SCHEMATIC_ID}" || "${SCHEMATIC_ID}" == "null" ]]; then
  echo "failed to create image factory schematic" >&2
  exit 1
fi

ISO_URL="${FACTORY_API_URL}/image/${SCHEMATIC_ID}/v${QEMU_TALOS_VERSION}/metal-amd64.iso"

echo "==> Provisioning ${MACHINE_COUNT} QEMU machines (maintenance mode, joining Omni)"
# --skip-injecting-config leaves the nodes in maintenance mode; they join Omni purely via the
# SideroLink kernel args carried by the ISO. Omni installs the cluster's Talos version on
# allocation, so the talosctl-side roles here (all "control planes") are irrelevant.
"${TALOSCTL}" cluster create dev \
  --provisioner=qemu \
  --name="${CLUSTER_NAME}" \
  --controlplanes="${MACHINE_COUNT}" \
  --workers=0 \
  --mtu=1430 \
  --memory="${QEMU_MEMORY}" \
  --cpus="${QEMU_CPUS}" \
  --with-uuid-hostnames \
  --cidr="${QEMU_CIDR}" \
  --skip-injecting-config \
  --wait=false \
  --iso-path="${ISO_URL}"

echo "==> Running machine-backed acceptance test against ${OMNI_ENDPOINT}"
go test -tags qemu ./internal/integration/... -run TestAccOmniClusterWithMachines -v -count=1 -timeout 60m "$@"
