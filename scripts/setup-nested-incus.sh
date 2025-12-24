#!/usr/bin/env bash
set -euo pipefail

# Required: path to client certificate to inject
CLIENT_CERT=""

# Setup a nested Incus container for testing incus-compose
# This creates an isolated Incus instance accessible via HTTPS

# Default values
CONTAINER_NAME="incuscompose-test"
IMAGE="images:debian/trixie"
INCUS_REPO="stable" # stable or lts
FORCE="false"

# Track whether we created the container so we can cleanup on failure if desired
CONTAINER_CREATED="false"

cleanup() {
    local rc=$?
    if [[ "${CONTAINER_CREATED}" == "true" && "${FORCE}" == "true" ]]; then
        echo "Cleaning up created container ${CONTAINER_NAME} due to error (exit ${rc})..."
        incus delete --force "${CONTAINER_NAME}" >/dev/null 2>&1 || true
    fi
    return $rc
}
trap cleanup EXIT

# Usage information
usage() {
    cat <<EOF
Usage: $(basename "$0") -c CERT [OPTIONS]

Setup a nested Incus container for testing incus-compose.

REQUIRED:
-c CERT         Path to client certificate to inject into trust store

OPTIONS:
-n NAME         Container name (default: ${CONTAINER_NAME})
                Note: Dots will be replaced with hyphens (DNS-safe)
-i IMAGE        Base image (default: ${IMAGE})
-r REPO         Incus repository: stable or lts (default: ${INCUS_REPO})
-f              Force delete any existing container (default: false)
-h              Show this help message

EXAMPLES:
# Create with defaults (stable version)
$(basename "$0") -c test/certs/incuscompose-test.crt

# Create with LTS version
$(basename "$0") -c test/certs/incuscompose-test.crt -r lts

# Create with custom name
$(basename "$0") -c test/certs/my-test.crt -n my-test -r lts

EOF
    exit 0
}

# Parse arguments
while getopts "c:n:i:r:fh" opt; do
    case ${opt} in
    c)
        CLIENT_CERT="${OPTARG}"
        ;;
    n)
        CONTAINER_NAME="${OPTARG}"
        ;;
    i)
        IMAGE="${OPTARG}"
        ;;
    r)
        INCUS_REPO="${OPTARG}"
        ;;
    f)
        FORCE="true"
        ;;
    h)
        usage
        ;;
    \?)
        echo "Invalid option: -${OPTARG}" >&2
        echo "Use -h for help" >&2
        exit 1
        ;;
    :)
        echo "Option -${OPTARG} requires an argument" >&2
        exit 1
        ;;
    esac
done

# Validate required arguments
if [[ -z "${CLIENT_CERT}" ]]; then
    echo "Error: Client certificate (-c) is required" >&2
    echo "Use -h for help" >&2
    exit 1
fi

if [[ ! -f "${CLIENT_CERT}" ]]; then
    echo "Error: Certificate file not found: ${CLIENT_CERT}" >&2
    exit 1
fi

# Sanitize container name to be DNS-safe
CONTAINER_NAME="${CONTAINER_NAME//./-}"

shift $((OPTIND - 1))

# Validate repository selection early
case "${INCUS_REPO}" in
stable)
    REPO_URL="https://pkgs.zabbly.com/incus/stable"
    ;;
lts)
    REPO_URL="https://pkgs.zabbly.com/incus/lts-6.0"
    ;;
*)
    echo "Error: Unknown repository '${INCUS_REPO}'" >&2
    echo "Valid options: stable, lts" >&2
    exit 1
    ;;
esac

# Ensure incus CLI is available
if ! command -v incus >/dev/null 2>&1; then
    echo "Error: 'incus' CLI not found in PATH. Please install/incus or adjust PATH." >&2
    exit 1
fi

echo "==> Configuration:"
echo "    Container name: ${CONTAINER_NAME}"
echo "    Base image: ${IMAGE}"
echo "    Incus repository: ${INCUS_REPO}"
echo "    Repository URL: ${REPO_URL}"
echo "    Client certificate: ${CLIENT_CERT}"
echo ""

if incus info "${CONTAINER_NAME}" >/dev/null 2>&1; then
    if [[ $FORCE == "true" ]]; then
        echo "Deleting existing container ${CONTAINER_NAME} (force)"
        incus delete --force "${CONTAINER_NAME}"
    else
        echo "Error: Container ${CONTAINER_NAME} already exists."
        echo "Delete it first with: incus delete -f ${CONTAINER_NAME}"
        exit 1
    fi
fi

echo "==> Creating nested Incus container: ${CONTAINER_NAME}"

# Create container with nesting enabled
incus launch "${IMAGE}" "${CONTAINER_NAME}" \
    -c security.nesting=true \
    -c security.privileged=true

CONTAINER_CREATED="true"

INSTALL_SCRIPT=$(
    cat <<'EOF'
#!/bin/bash
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive

echo "Installing prerequisites..."
apt-get update -q
apt-get install -y -q curl gpg ca-certificates

echo "Adding Incus repository..."
mkdir -p /etc/apt/keyrings/
curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc

cat > /etc/apt/sources.list.d/zabbly-incus.sources <<SOURCES_EOF
Enabled: yes
Types: deb
URIs: REPO_URL_PLACEHOLDER
Suites: $(. /etc/os-release && echo ${VERSION_CODENAME})
Components: main
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/zabbly.asc
SOURCES_EOF

echo "Installing Incus..."
apt-get update -q
apt-get install -y -q incus-base

echo "Incus installed successfully!"
EOF
)

echo "==> Executing installation script"
# Keep your variable-based pipe approach; replace placeholder and stream into container
printf "%s" "${INSTALL_SCRIPT}" | sed "s|REPO_URL_PLACEHOLDER|${REPO_URL}|g" | incus exec "${CONTAINER_NAME}" -- bash -s

echo "==> Executing Incus init script"

CONFIGURE_SCRIPT=$(
    cat <<'EOF'
#!/bin/bash
set -euo pipefail

echo "Starting Incus daemon..."
systemctl enable --now incus.socket || true

echo "Waiting for Incus to be ready..."
# incus admin waitready exists on newer installs; fall back to a small loop if necessary.
if incus admin waitready --timeout=60 >/dev/null 2>&1; then
    echo "Incus admin reports ready"
else
    echo "Waiting for Incus socket by polling..."
    for i in {1..30}; do
        if incus info >/dev/null 2>&1; then
            break
        fi
        sleep 1
    done
fi

echo "Initializing Incus..."
cat <<PRESEED_EOF | incus admin init --preseed
config:
  core.https_address: "[::]:8443"
networks:
- name: incusbr0
  type: bridge
  config:
    ipv4.address: auto
    ipv6.address: none
storage_pools:
- name: default
  driver: dir
profiles:
- name: default
  devices:
    root:
      path: /
      pool: default
      type: disk
    eth0:
      name: eth0
      network: incusbr0
      type: nic
PRESEED_EOF

echo "Incus configuration complete!"
echo "Version: $(incus version || true)"
echo "Listening on: $(incus config get core.https_address || true)"
EOF
)

# Stream the configure script as well (no temp files)
printf "%s" "${CONFIGURE_SCRIPT}" | incus exec "${CONTAINER_NAME}" -- bash -s

# Inject client certificate into trust store
echo "==> Adding client certificate to nested Incus trust store"
incus file push -- "${CLIENT_CERT}" "${CONTAINER_NAME}/root/client.crt"
incus exec "${CONTAINER_NAME}" -- incus config trust add-certificate /root/client.crt --restricted=false
incus exec "${CONTAINER_NAME}" -- rm -f /root/client.crt
echo "    Certificate added with unrestricted access"
echo ""
echo -e "==> Container ready:\n\n"
incus info "${CONTAINER_NAME}"
