# incus-compose

Bring the familiar Docker Compose workflow to Incus containers. `incus-compose` implements the Compose specification for the Incus ecosystem, allowing you to define and run multi-container applications using the same `docker-compose.yml` files you already know.

## Quick Links

- **[Getting Started](docs/getting-started.md)** - Install and run your first compose project
- **[CLI Reference](docs/cli.md)** - Commands and options
- **[Compose Compatibility](docs/compose-compatibility.md)** - What works and what doesn't
- **[Architecture](docs/architecture.md)** - How it works under the hood
- **[Why Incus?](docs/why-incus.md)** - Benefits over Docker

[Full Documentation](docs/) | [Contributing](CONTRIBUTING.md)

## Status

**Active Development** - Core functionality is working and well-tested. The API is stabilizing but may still change.

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

## Why incus-compose?

[Incus](https://linuxcontainers.org/incus/) provides powerful system containers and virtual machines with superior security and isolation, but lacks the declarative multi-container orchestration that Docker Compose offers. This tool bridges that gap:

- Use existing `docker-compose.yml` files with Incus containers
- Leverage Incus's native OCI registry support for image pulling
- Run Docker/OCI images directly from registries
- Manage complex multi-container applications with familiar commands
- Benefit from Incus's resource efficiency and security model

## Architecture

incus-compose uses a **resource-first design**:

- **Unified Resource Interface** - Images, instances, networks, profiles, and volumes are all first-class resources
- **Two-Phase Pattern** - Configuration (resource creation) then execution (ensure/start/stop/delete)
- **Priority-Based Ordering** - Dependencies managed via numeric priorities, no complex graph resolution
- **Stack Execution** - Batch operations with parallel image downloads
- **Hook System** - Before/after action interception for logging and validation

See [Architecture Documentation](docs/architecture.md) for details.

## Quick Start

### Prerequisites

```bash
# Add OCI image remotes to Incus
incus remote add --protocol oci docker.io https://docker.io
incus remote add --protocol oci ghcr.io https://ghcr.io
incus remote add --protocol oci registry.gitlab.com https://registry.gitlab.com
```

### Installation

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

## Design Principles

**KISS** - Keep It Simple, Stupid. We prefer:

- Shallow package structure over deep nesting
- Direct code over abstractions
- Working software over perfect architecture
- Simple solutions over clever ones
- Boring code that's easy to understand

**Compose Specification First** - We follow the [Compose specification](https://compose-spec.io/) faithfully. Any deviation from Docker Compose behavior is either:

- Intentional and documented in [Compose Compatibility](docs/compose-compatibility.md)
- A bug that should be reported

**Incus Native** - We use Incus's official Go client library and leverage its native features (OCI support, projects, profiles, storage pools).

See [Contributing](CONTRIBUTING.md) for detailed guidelines.

## Development

```bash
# Set up development environment
just dev-install

# Run tests
just test

# Run linters
just lint

# Build binary
just build

# Run against a compose file
just run -f test/fixtures/simple-nginx/compose.yaml config
```

## Library Usage

The `client` and `project` packages can be used programmatically:

```go
import (
    "context"
    "gitlab.com/r3j0/incus-compose/client"
    "gitlab.com/r3j0/incus-compose/project"
)

// Create client
globalClient := client.New(ctx,
    client.ClientURL("unix:///var/lib/incus/unix.socket"),
)
globalClient.Connect()

// Load compose project
proj, _ := project.Load("compose.yaml")

// Execute
// ... see docs/architecture/client/ for full API
```

**Note:** The library API is not yet stable and may change.

See [Client Package](docs/architecture/client/) for details.

## Credits

This project builds on work by [@bketelsen](https://github.com/bketelsen).
Some components are adapted from [docker compose](https://github.com/docker/compose).

This project uses AI tools as development aids (drafting, iteration, reviews, tests, and documentation).
Architecture, constraints, and final code decisions are owned by the human committers.

## License

[Apache 2.0](LICENSE)
