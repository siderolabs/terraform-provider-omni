#!/usr/bin/env bash
#
# Bring up a throwaway Omni instance, extract the bootstrapped service-account key, and run the
# provider acceptance tests against it. Tears everything down on exit.
#
# Usage: hack/test/run.sh [go test args...]
#   OMNI_VERSION   Omni image tag to test against (default: latest).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

# Point the provider tests at an existing Terraform CLI. Without one, terraform-plugin-testing
# installs its own copy into a fresh temp dir for every test, and with the tests running in parallel
# a fork can inherit a write handle to a binary another test is still extracting, so the exec fails
# with "text file busy". Reusing one binary writes nothing, so the race cannot happen.
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

PROJECT=omni-tf-acc
COMPOSE_FILE=hack/test/docker-compose.yaml
COMPOSE=(docker compose -p "${PROJECT}" -f "${COMPOSE_FILE}")

# Host port to publish Omni's API on (override if 8099 is already taken locally).
export OMNI_HOST_PORT="${OMNI_HOST_PORT:-8099}"

# Host port to publish the SeaweedFS S3 gateway on (only used for debugging from the host; Omni
# reaches it over the compose network).
export S3_HOST_PORT="${S3_HOST_PORT:-8333}"

# S3 settings the etcd backup acceptance test configures Omni with. The credentials must match the
# identity in hack/test/s3-config.json.
export OMNI_TEST_S3_ENDPOINT="${OMNI_TEST_S3_ENDPOINT:-http://s3:8333}"
export OMNI_TEST_S3_REGION="${OMNI_TEST_S3_REGION:-us-east-1}"
export OMNI_TEST_S3_BUCKET="${OMNI_TEST_S3_BUCKET:-tf-acc-etcd-backups}"
export OMNI_TEST_S3_ACCESS_KEY_ID="${OMNI_TEST_S3_ACCESS_KEY_ID:-tfaccesskey}"
export OMNI_TEST_S3_SECRET_ACCESS_KEY="${OMNI_TEST_S3_SECRET_ACCESS_KEY:-tfsecretkey}"

# No external machines join in the machine-less suite, so advertise SideroLink on loopback. Render
# the Omni config from its template (see hack/test/omni-config.yaml.tmpl).
export OMNI_SIDEROLINK_ADVERTISED_HOST="${OMNI_SIDEROLINK_ADVERTISED_HOST:-127.0.0.1}"
envsubst '${OMNI_SIDEROLINK_ADVERTISED_HOST}' < hack/test/omni-config.yaml.tmpl > hack/test/omni-config.yaml

cleanup() {
  "${COMPOSE[@]}" logs --no-color > /tmp/omni-tf-acc.log 2>&1 || true
  "${COMPOSE[@]}" down -t 5 -v --remove-orphans || true
}
trap cleanup EXIT INT TERM

echo "==> Starting Omni, mock OIDC and S3"
# Remove any leftover state from a previous run so Omni bootstraps fresh (initial users + key).
"${COMPOSE[@]}" down -t 5 -v --remove-orphans >/dev/null 2>&1 || true
"${COMPOSE[@]}" up -d

OMNI_CID="$("${COMPOSE[@]}" ps -q omni)"
if [[ -z "${OMNI_CID}" ]]; then
  echo "failed to determine Omni container id" >&2
  exit 1
fi

# SeaweedFS does not create buckets on demand, and Omni validates a backup configuration by listing
# the bucket, so both the initial and the post-update bucket must exist up front.
echo "==> Creating S3 buckets"
s3_deadline=$(( $(date +%s) + 120 ))
until "${COMPOSE[@]}" exec -T s3 sh -c \
  "printf 's3.bucket.create -name %s\ns3.bucket.create -name %s-updated\n' '${OMNI_TEST_S3_BUCKET}' '${OMNI_TEST_S3_BUCKET}' | weed shell -master=localhost:9333" >/dev/null 2>&1; do
  if [[ $(date +%s) -gt ${s3_deadline} ]]; then
    echo "S3 buckets could not be created in time" >&2
    "${COMPOSE[@]}" logs --no-color s3 | tail -50 >&2 || true
    exit 1
  fi
  sleep 2
done

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

echo "==> Running acceptance tests against ${OMNI_ENDPOINT}"
TF_ACC=1 go test ./pkg/omni/... -run 'TestAcc' -v -count=1 "$@"
