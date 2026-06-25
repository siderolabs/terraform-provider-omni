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

PROJECT=omni-tf-acc
COMPOSE_FILE=hack/test/docker-compose.yaml
COMPOSE=(docker compose -p "${PROJECT}" -f "${COMPOSE_FILE}")

# Host port to publish Omni's API on (override if 8099 is already taken locally).
export OMNI_HOST_PORT="${OMNI_HOST_PORT:-8099}"

cleanup() {
  "${COMPOSE[@]}" logs --no-color > /tmp/omni-tf-acc.log 2>&1 || true
  "${COMPOSE[@]}" down -t 5 -v --remove-orphans || true
}
trap cleanup EXIT INT TERM

echo "==> Starting Omni and mock OIDC"
# Remove any leftover state from a previous run so Omni bootstraps fresh (initial users + key).
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

echo "==> Running acceptance tests against ${OMNI_ENDPOINT}"
TF_ACC=1 go test ./pkg/omni/... -run 'TestAcc' -v -count=1 "$@"
