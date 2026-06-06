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

- **[Getting Started](docs/getting-started.md)** - Install and run your first compose project
- **[CLI Reference](docs/cli.md)** - Commands and options
- **[Compose Compatibility](docs/compose-compatibility.md)** - What works and what doesn't
- **[Architecture](docs/architecture.md)** - How it works under the hood
- **[Why Incus?](docs/why-incus.md)** - Benefits over Docker

[Full Documentation](docs/architecture.md) | [Contributing](CONTRIBUTING.md)

## Status

**Beta** - testing the beta1 release of incus-compose.

**What works:**

- `up`, `down`, `list` (and `ps`), `start`, `stop`, `restart`, `exec`, `config`, `logs` commands
- Compose project parsing via compose-go
- OCI image pulling from docker.io, ghcr.io, and other registries
- Bridge networks with automatic name sanitization
- Storage volumes with UID/GID shifting for proper permissions
- Bind mounts (local connections only)
- Port forwarding via proxy devices
- Incus project isolation

**What's coming:**

- VM instance support alongside containers
- Container image building via Podman/Docker
- Advanced compose features (healthchecks, resource limits, etc.)

## Architecture

incus-compose uses a **resource-first design**, see [Architecture Documentation](docs/architecture.md) for details.

## Quick Start

### Prerequisites

Switch to a https remote (required for healthchecking).

incus-compose auto-detects the bridge healthd should use (incusbr0, then the default profile's eth0).
Use --healthd-network or x-incus-compose.healthd-network if your setup differs — see [Network Configuration](docs/healthd.md#network-configuration).

1.) Get the IP Address of your main bridge (incusbr0 or the one in the default profile).

```bash
incus network list
```

2.) Either set that IP as `$IP:8443` or listen on all interface `:8443`

```bash
incus config set core.https_address=<ip>:8443
```

3.) Generate a cert and add it to the trust store as admin cert.

```bash
# Generate and trust a certificate
incus remote generate-certificate
incus config trust add-certificate ~/.config/incus/client.crt

incus remote add local-https <ip>
# or
incus remote add local-https 127.0.0.1

# Switch to local-https as default remote
incus remote switch local-https
```

Add OCI image remotes to Incus

```bash
incus remote add --protocol oci docker.io https://docker.io
incus remote add --protocol oci ghcr.io https://ghcr.io
incus remote add --protocol oci registry.gitlab.com https://registry.gitlab.com
```

### Installation

Binary:

https://gitlab.com/r3j0/incus-compose/-/releases

Source:

```bash
# Build from source
git clone https://gitlab.com/r3j0/incus-compose
cd incus-compose
just build

# Or install directly
go install gitlab.com/r3j0/incus-compose/cmd/incus-compose@latest
```

### Usage

```bash
# Create a compose.yaml
cat > compose.yaml <<EOF
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
    volumes:
      - web-data:/usr/share/nginx/html

volumes:
  web-data:
EOF

# Start services
incus-compose up

# View logs
incus-compose logs -f

# List running services
incus-compose list

# Stop and remove
incus-compose down
```

See [Getting Started](docs/getting-started.md) for detailed examples.

## Credits

This project builds on work by [@bketelsen](https://github.com/bketelsen).
Some components are adapted from [docker compose](https://github.com/docker/compose).

This project uses AI tools as development aids (drafting, iteration, reviews, tests, and documentation).
Architecture, constraints, and final code decisions are owned by the human committers.

## License

[Apache 2.0](LICENSE)
