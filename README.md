# incus-compose

Bring the familiar Docker Compose workflow to Incus containers. `incus-compose` implements the Compose specification for the Incus ecosystem, allowing you to define and run multi-container applications using the same `docker-compose.yml` files you already know.

## Why incus-compose?

[Incus](https://linuxcontainers.org/incus/) provides powerful system containers and virtual machines with superior security and isolation, but lacks the declarative multi-container orchestration that Docker Compose offers. This tool bridges that gap:

- Use existing `docker-compose.yml` files with Incus containers
- Leverage Incus's native OCI registry support for image pulling
- Run Docker/OCI images directly from registries
- Manage complex multi-container applications with familiar commands
- Benefit from Incus's resource efficiency and security model

## Quick Links

Full docs index: [docs/README.md](docs/README.md)

- **[Getting Started](docs/getting-started.md)** - Install and run your first compose project
- **[CLI Reference](docs/cli.md)** - Commands and options
- **[Compose Compatibility](docs/compose-compatibility.md)** - What works and what doesn't
- **[Architecture](docs/architecture.md)** - How it works under the hood
- **[Why Incus?](docs/why-incus.md)** - Benefits over Docker
- **[Contributing](CONTRIBUTING.md)** - Contributing to incus-compose

## Features

Status: **Beta** - testing the beta release of incus-compose.

- Familiar commands: `up`, `down`, `start`, `stop`, `restart`, `list` (and `ps`), `logs`, `exec`, `config`, plus `build`, `healthd`, `incus` (pass-through), and `self-update`
- Compose project parsing via compose-go, with automatic `compose.incus.yaml` overrides and `x-incus` / `x-incus-compose` extensions for raw Incus options
- OCI image pulling from docker.io, ghcr.io, and other registries
- Two-stage image cache in a dedicated Incus project (survives `down`/`up`, avoids registry rate limits)
- Local image building via Podman/Docker [doc](docs/build.md)
- Bridge networks with automatic name sanitization
- Static IPv4/IPv6 addresses with automatic DHCP ranges [doc](docs/compose-compatibility.md#automatic-dhcp-ranges)
- Port forwarding via proxy devices or kernel NAT mode
- Storage volumes with UID/GID shifting; bind mounts (pass-through by default, optional seeding)
- Health checks, restart policies, and `depends_on: service_healthy` ordering via the `ic-healthd` sidecar [doc](docs/healthd.md)
- Service scaling with `up --scale` and orphan pruning
- Incus project isolation
- Resource limits and other advanced compose features (`shm_size`, `container_name`, etc.)
- Configuration via `INCUS_COMPOSE_*` environment variables for every flag, with a configurable parallel worker count [doc](docs/environment-variables.md)

## Architecture

incus-compose uses a **resource-first design**, see [Architecture Documentation](docs/architecture.md) for details.

## Quick Start

Requires `podman` or `docker` for image building and an Incus https remote (needed for healthchecking) with OCI registries added.
See [Getting Started](docs/getting-started.md) for the full setup walkthrough.

Install the latest release (the script verifies the SHA-256 checksum):

```bash
curl -sSfL https://raw.githubusercontent.com/lxc/incus-compose/main/install.sh | sh -s -- -b ~/.local/bin
```

Or grab a prebuilt archive from the [Releases Page](https://github.com/lxc/incus-compose/releases).

Then point it at your existing `compose.yaml`:

```bash
# Start services
incus-compose up

# View logs
incus-compose logs -f

# List running services
incus-compose list

# Stop and remove
incus-compose down
```

## Credits

This project is inspired by [@bketelsen](https://github.com/bketelsen/incus-compose).
Some components are adapted from [docker compose](https://github.com/docker/compose).
The `install.sh` script is adapted from [golangci-lint](https://github.com/golangci/golangci-lint), based on the [GoReleaser](https://goreleaser.com) install-script template.

This project uses AI tools as development aids (drafting, iteration, reviews, tests, and documentation).
Architecture, constraints, and final code decisions are owned by the human committers.

Earlier development was on [Gitlab](https://gitlab.com/r3j0/incus-compose/).

## License

[Apache 2.0](LICENSE)
