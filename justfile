# incus-compose development commands
#
# Quick start:
#   just dev-install    # Set up nested Incus for testing
#   just run <args>     # Run incus-compose against nested Incus
#   just test           # Run tests against nested Incus
#
# Local development (uses your real Incus - be careful!):
#   just run-local <args>
#   just test-local

set dotenv-load
set shell := ["bash", "-euo", "pipefail", "-c"]
# Pass args as real positional parameters so "$@" preserves shell metacharacters
# (e.g. a -run 'TestA|TestB' pattern is not split on the pipe).
set positional-arguments

v_test_procs := env("TEST_PROCS", "2")

[private]
default:
    @just --list

cleanup:
    just purge-projects
    just purge-networks || true
    sudo systemctl restart incus.service incus.socket

# Run tests against nested Incus, includes direct incus tests.
[env("INCUS_COMPOSE_IMAGE_CACHE", "incus-compose-tests-cache")]
test folder="./..." *args:
    export DATE=`date +%Y%m%d-%H%M%S`; \
      gotestsum --hide-summary=skipped --format dots-v2 --jsonfile=test/logs/${DATE}.json --packages={{ folder }} \
        --post-run-command "bash -c 'echo; echo Slowest tests; gotestsum tool slowest --num 10 --jsonfile test/logs/${DATE}.json'" \
        -- -parallel {{ v_test_procs }} -timeout 20m -covermode atomic -coverprofile test/logs/${DATE}-cover.out -v "${@:2}"; \
# Run local unit-tests, incus-facing tests are skipped.
[env("INCUS_COMPOSE_TEST_LOCAL", "1")]
test-local folder="./..." *args:
    @just test "$@"

# Run e2e tests.
[env("INCUS_COMPOSE_TEST_E2E", "1")]
test-e2e folder="./..." *args:
    @just test "$@"

# Run example tests.
[env("INCUS_COMPOSE_TEST_EXAMPLES", "1")]
test-examples *args:
    @just test ./examples/... "$@"

# Run all tests, includes direct incus, e2 and examples tests.
[env("INCUS_COMPOSE_TEST_E2E", "1")]
[env("INCUS_COMPOSE_TEST_EXAMPLES", "1")]
test-all folder="./..." *args:
    @just test "$@"

# Run the tests with a race detector.
test-race folder="./..." *args:
    gotestsum --hide-summary=skipped --jsonfile=test/logs/`date +%Y%m%d-%H%M%S`.json --packages={{ folder }} -- -parallel {{ v_test_procs }} -timeout 20m -race -v "${@:2}"

# Update snapshots for long running tests.
[env("INCUS_COMPOSE_TEST_E2E", "1")]
[env("UPDATE_SNAPSHOTS", "1")]
update-e2e-snapshots folder="./..." *args:
    @just test "$@"

# Update snapshots for examples.
[env("INCUS_COMPOSE_TEST_EXAMPLES", "1")]
[env("UPDATE_SNAPSHOTS", "1")]
update-examples-snapshots folder="./..." *args:
    @just test "$@"

# Update snapshot test files that require a remote
[env("UPDATE_SNAPSHOTS", "1")]
update-snapshots folder="./..." *args:
    @just test "$@"

[private]
log-run logfile="" cmd="":
    @(time {{ cmd }}) 2>&1 | tee -a {{ logfile }} || EXIT_CODE=$?; \
    echo -e "\n\nCMD: {{ cmd }}\nLog: {{ logfile }}" | tee -a {{ logfile }}; \
    exit ${EXIT_CODE:-0}

# Lint all files.
lint folder="./...":
    shellcheck **/*.sh
    golangci-lint run {{ folder }}

# Lint and fix all files.
fix folder="./...":
    golangci-lint run --fix {{ folder }}

# Dev install creates your dev environment: `just dev-install [container] [listen] [project] [image]`
dev-install container_name="local:ict" listen='127.0.0.1:1443' project='default' image='images:debian/trixie' storagepool='default' repo='stable':
    go install gotest.tools/gotestsum@latest
    @just make-nested "{{ container_name }}" "{{ image }}" "{{ listen }}" "{{ project }}" "{{ storagepool }}" "{{ repo }}"
    just build-healthd-image

# Run commands in the nested incus.
incus *args:
    @echo "Using remote '${INCUS_REMOTE-"local"}':"
    incus {{ args }}

# Build a release binary
build-release: lint
    go build -ldflags="-w -s -X github.com/lxc/incus-compose/cmd/incus-compose/version.Version=`git describe --tags --always --long --dirty="-dirty"`" -o bin/incus-compose ./cmd/incus-compose

# Build a dev binary
build: lint
    go build -o bin/incus-compose ./cmd/incus-compose

# Build ic-healthd binary
build-healthd: lint
    CGO_ENABLED=0 go build -tags=netgo -ldflags="-w -s -X github.com/lxc/incus-compose/cmd/ic-healthd/version.Version=`git describe --tags --always --long --dirty="-dirty"`" -trimpath -o bin/ic-healthd ./cmd/ic-healthd

# Build ic-healthd container image
build-healthd-image tag_base="ghcr.io/lxc/incus-compose/ic-healthd":
    #!/usr/bin/env bash
    set -euo pipefail

    export VERSION=`git describe --tags --always --long --dirty="-dirty"`
    echo ${VERSION}
    echo "Building for the 'default' cache"
    just run -P cmd/ic-healthd build --os-env
    echo "Building for the 'incus-compose-tests-cache' cache"
    INCUS_COMPOSE_IMAGE_CACHE="incus-compose-tests-cache" just run -P cmd/ic-healthd build --os-env
    sed -i -e 's|export INCUS_COMPOSE_HEALTHD_UP_IMAGE=".*"|export INCUS_COMPOSE_HEALTHD_UP_IMAGE="{{ tag_base }}:'${VERSION}'"|g' \
           -e 's|export INCUS_COMPOSE_UP_IMAGE=".*"|export INCUS_COMPOSE_UP_IMAGE="{{ tag_base }}:'${VERSION}'"|g' \
           .env

# Build ic-healthd container image
release-healthd-image tag="ghcr.io/lxc/incus-compose/ic-healthd:latest": build-healthd-image
    echo "${GITHUB_TOKEN}" | podman login --username "${GITHUB_USERNAME}" --password-stdin ghcr.io
    podman push {{ tag }}

# Release incus-compose
release tag="0.0.1-dev0" healthd_image="ghcr.io/lxc/incus-compose/ic-healthd": build-healthd-image
    git tag v{{ tag }}
    echo "${GITHUB_TOKEN}" | podman login --username "${GITHUB_USERNAME}" --password-stdin ghcr.io
    podman push {{ healthd_image }}:latest
    podman tag {{ healthd_image }}:latest {{ healthd_image }}:{{ tag }}
    podman push {{ healthd_image }}:{{ tag }}
    goreleaser release --clean
    git push --tags

# Run with local healthd binary (for testing without an explicit OCI image) (ex. just run-healthd -f test/healthd/debug/compose.yaml up )
run-healthd compose="examples/immich/compose.yaml" name="immich": build-healthd
    go run ./cmd/incus-compose --debug -f {{ compose }} healthd up --recreate --binary bin/ic-healthd
    go run ./cmd/incus-compose -f {{ compose }} incus exec {{ name }}-ic-healthd -- tail -n 1000 -f /var/log/ic-healthd.log

# Usage: just run -f test/fixtures/simple-nginx/compose.yaml config
run *args:
    @go run ./cmd/incus-compose {{ args }}

# Purge all dangling networks (managed and 0 users) from the configured remote
purge-networks:
    #!/usr/bin/env bash
    set -euo pipefail

    remote="${INCUS_REMOTE:-local}"
    networks=$(incus network list "${remote}:" -f json | jq -r '.[] | select(.used_by | length == 0) | select(.managed == true) | .name')

    if [[ -z "${networks}" ]]; then
        echo "No dangling networks found."
        exit 1
    fi

    echo "Deleting dangling networks on remote '${remote}':"
    while IFS= read -r network; do
        echo "  Deleting: ${network}"
        incus network delete "${remote}:${network}"
    done <<< "${networks}"
    echo "Done."

# Removes all images
purge-images *args:
    #!/usr/bin/env bash
    set -euo pipefail

    remote="${INCUS_REMOTE:-local}"
    images=$(incus image list "${remote}:" {{ args }} -f json | jq -r '.[] .fingerprint')

    if [[ -z "${images}" ]]; then
        echo "No images found."
        exit 1
    fi

    echo "Deleting images on remote '${remote}':"
    while IFS= read -r image; do
        echo "  Deleting: ${image}"
        incus image delete {{ args }} "${remote}:${image}"
    done <<< "${images}"
    echo "Done."

# Removes all projects
purge-projects:
    #!/usr/bin/env bash
    set -euo pipefail

    remote="${INCUS_REMOTE:-local}"
    projects=$(incus project list "${remote}:" -f json | jq -r '.[] .name')

    echo "Deleting projects on remote '${remote}':"
    while IFS= read -r project; do
        if [[ $project != "default" ]] && [[ $project != "incus-compose-tests-cache" ]] then
          echo "  Deleting: ${project}"
          echo -e "yes\n" | incus project delete -f "${remote}:${project}"
        fi
    done <<< "${projects}"
    echo "Done."

purge-tokens:
    #!/usr/bin/env bash
    set -euo pipefail

    remote="${INCUS_REMOTE:-local}"
    tokens=$(incus config trust list-tokens "${remote}:" -f json | jq -r '.[] .client_name')

    if [[ -z "${tokens}" ]]; then
        echo "No tokens found."
        exit 1
    fi

    echo "Revoking tokens on remote '${remote}':"
    while IFS= read -r token; do
        echo "  Revoking: ${token}"
        incus config trust revoke-token "${remote}:${token}"
    done <<< "${tokens}"
    echo "Done."

# Run this before you commit/push.
pre-commit:
    go mod tidy
    rg -q "// TODO" **/*.go || exit 0
    just lint
    just test

push: pre-commit
    git push

[private]
make-nested container='local:ict' image='images:debian/trixie' listen="127.0.0.1:1443" project="default" storagepool="default" repo="stable":
    #!/usr/bin/env bash
    set -euo pipefail

    container="{{ container }}"
    image="{{ image }}"
    listen="{{ listen }}"
    storagepool="{{ storagepool }}"
    repo="{{ repo }}"

    key_file=""
    cert_file=""
    if [[ -f $HOME/.config/incus/client.crt ]] && [[ -f $HOME/.config/incus/client.key ]]; then
        key_file="$HOME/.config/incus/client.key"
        cert_file="$HOME/.config/incus/client.crt"
    fi

    # Run setup script (certificate injection is handled by the script)
    set +e
    echo "Trying to create a nested container:\n"
    INCUS_PROJECT="{{ project }}" ./scripts/setup-nested-incus.sh -c "${cert_file}" -n "${container}" -i "${image}" -r "${repo}" -l "${listen}" -p "${storagepool}"
    set -e

    if [[ -z "${listen}" ]]; then
        container_ip=$(incus list "${container}" -c4 --format json 2>/dev/null | jq -r '.[0].state.network // {} | [ .[].addresses[]? | select(.family == "inet") | .address ] | .[0] // empty' 2>/dev/null)
        if [ -z "${container_ip}" ]; then
            echo "Error: Could not get container IP"
            echo "This scripts requires you to setup the nested incus instance first, use 'just make-nested' to create it."
            exit 1
        fi

        url="https://${container_ip}:8443"
    else
        url="https://${listen}"
    fi

    INCUS_REMOTE="${container%%:*}" incus remote remove "${container##*:}" || true
    incus remote add "${container##*:}" "${url}" --accept-certificate
