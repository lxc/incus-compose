#!/usr/bin/env bash
set -euo pipefail
# Copyright (c) 2026 René Jochum <rene@jochum.dev>
# This script is released into the public domain or under CC0-1.0.
# Use it however you want, no restrictions.
#
# Usage: ./scripts/test-upgrade.sh [FIXTURE] [REMOTE]
#
# Tests upgrading incus-compose across releases:
#   1. Deletes "incus-compose" project and "icompose0" network (clean state)
#   2. Downloads v1.0.0 and v1.2.0 binaries (using install.sh)
#   3. Runs "just build" to build dev binary and sidecar images
#   4. Runs "incus-compose -P <fixture> up -d" on v1.0.0, v1.2.0, then "just run -P <fixture> up -d"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

FIXTURE="${1:-test/fixtures/two-services}"
export INCUS_REMOTE="${2:-${INCUS_REMOTE:-local}}"

if [[ ! -d "${FIXTURE}" && -d "${PROJECT_ROOT}/${FIXTURE}" ]]; then
  FIXTURE="${PROJECT_ROOT}/${FIXTURE}"
fi

if [[ ! -f "${FIXTURE}/compose.yaml" && ! -f "${FIXTURE}/docker-compose.yaml" ]]; then
  echo "Error: no compose file found in ${FIXTURE}" >&2
  exit 1
fi

PROJECT_NAME="$(basename "${FIXTURE}")"
BIN_DIR="${PROJECT_ROOT}/work/upgrade-bin"

echo "Incus Compose Upgrade Test"
echo "Fixture: ${FIXTURE}"
echo "Project: ${PROJECT_NAME}"
echo "Remote:  ${INCUS_REMOTE}"
echo ""

# 1. Clean up existing resources on the remote
echo "Cleaning up previous state on '${INCUS_REMOTE}'"

# Delete existing fixture project if present
if incus project show "${PROJECT_NAME}" &>/dev/null; then
  echo "Deleting existing fixture project '${PROJECT_NAME}'..."
  echo yes | incus project delete -f "${PROJECT_NAME}" || true
fi

# Delete icompose0 if present in incus-compose project
if INCUS_PROJECT=incus-compose incus network show icompose0 &>/dev/null; then
  echo "Deleting icompose0 network in incus-compose project..."
  INCUS_PROJECT=incus-compose incus network delete icompose0 || true
fi

# Delete incus-compose project (prompt requires 'yes')
if incus project show incus-compose &>/dev/null; then
  echo "Deleting incus-compose project..."
  echo yes | incus project delete -f incus-compose || true
fi

# Delete icompose0 in default project or remote-level if still lingering
if INCUS_PROJECT=default incus network show icompose0 &>/dev/null; then
  echo "Deleting legacy icompose0 network in default project..."
  INCUS_PROJECT=default incus network delete icompose0 || true
fi
if incus network show icompose0 &>/dev/null; then
  echo "Deleting icompose0 network..."
  incus network delete icompose0 || true
fi

# 2. Download and prepare binaries
echo ""
echo "Preparing binaries in ${BIN_DIR}"

download_binary() {
  local version="$1"
  local dest_dir="${BIN_DIR}/${version}"
  local dest_bin="${dest_dir}/incus-compose"

  if [[ -x "${dest_bin}" && "${FORCE_DOWNLOAD:-0}" != "1" ]]; then
    echo "Using cached binary for ${version}: ${dest_bin}"
    return 0
  fi

  echo "Downloading ${version} via install.sh..."
  mkdir -p "${dest_dir}"
  "${PROJECT_ROOT}/install.sh" -b "${dest_dir}" "${version}"
}

download_binary "v1.0.0"
download_binary "v1.2.0"

if [[ -n "${VERSION:-}" ]]; then
  if [[ ! -f "${PROJECT_ROOT}/.env" ]]; then
    cp "${PROJECT_ROOT}/.env.sample" "${PROJECT_ROOT}/.env"
  fi
  sed -i -e 's|export VERSION=".*"|export VERSION="'"${VERSION}"'"|g' "${PROJECT_ROOT}/.env"
fi

echo ""
echo "Building current environment with 'just build'"
(cd "${PROJECT_ROOT}" && just build)

# 3. Sequentially run up -d on each binary and current
echo ""
echo "Running v1.0.0: incus-compose -P ${FIXTURE} up -d"
"${BIN_DIR}/v1.0.0/incus-compose" -P "${FIXTURE}" up -d

echo ""
echo "Running v1.2.0: incus-compose -P ${FIXTURE} up -d"
"${BIN_DIR}/v1.2.0/incus-compose" -P "${FIXTURE}" up -d

echo ""
echo "Running current: just run -P ${FIXTURE} up -d"
(cd "${PROJECT_ROOT}" && just run -P "${FIXTURE}" up -d)

# 4. Verify status after final upgrade
echo ""
echo "Verifying containers with current incus-compose"
(cd "${PROJECT_ROOT}" && just run -P "${FIXTURE}" ps)

echo ""
echo "Upgrade test completed successfully."

# 5. Optional cleanup
if [[ "${CLEANUP:-0}" == "1" ]]; then
  echo ""
  echo "Cleaning up fixture project..."
  (cd "${PROJECT_ROOT}" && just run -P "${FIXTURE}" down --project) || true
fi
