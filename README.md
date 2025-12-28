# incus-compose

Bring the familiar Docker Compose workflow to Incus containers. `incus-compose` implements the Compose specification for the Incus ecosystem, allowing you to define and run multi-container applications using the same `docker-compose.yml` files you already know.

## Quick Links

- **[Getting Started](docs/getting-started.md)** - Install and run your first compose project
- **[Compose Compatibility](docs/compose-compatibility.md)** - What works and what doesn't
- **[Environment Variables](docs/environment-variables.md)** - How env vars work
- **[Why Incus?](docs/why-incus.md)** - Benefits over Docker

[Full Documentation](docs/) | [Roadmap](docs/roadmap.md)

## Status

**Early Development** - This project is in its initial phase. APIs and behavior may change. Contributions and feedback are welcome!

It does "up, down and ps" those are well tested.

Compose projects get created with a incus project, storage pool volumes and as much bridge networks as you wish. Yes, it does also shift your Volumes transparently and it does bind mounts.

No specials included (caps and so on).

## Why incus-compose?

[Incus](https://linuxcontainers.org/incus/) provides powerful system containers and virtual machines, but lacks the declarative multi-container orchestration that Docker Compose offers. This tool bridges that gap, letting you:

- Use existing `docker-compose.yml` files with Incus containers
- Leverage the superior security and isolation model of Incus
- Run Docker/OCI images directly from registries like docker.io and ghcr.io
- Manage complex multi-container applications with familiar commands

## Goals

### Specification Compliance

- Parse and execute compose projects according to the [Compose specification](https://compose-spec.io/) using [compose-go](https://github.com/compose-spec/compose-go)
- Support the latest compose file format features
- Maintain compatibility with Docker Compose workflows

### Incus Integration

- Interact with Incus through its official Go client library
- Leverage Incus's native OCI registry support for image pulling
- Support both system containers and VM instances where applicable

### Command Compatibility

- Implement core `docker compose` commands: `up`, `down`, `start`, `stop`, `restart`, `logs`, `ps`, `exec`, and more
- Match Docker Compose CLI behavior and options where possible
- Document all intentional differences from Docker Compose
- Treat unexpected behavior differences as bugs

### Container Building

- Build container images using Podman (preferred) or Docker via their respective sockets
- Support both local Dockerfiles and remote build contexts

### Quality Assurance

- Comprehensive unit test coverage for core functionality
- End-to-end tests validating real-world compose scenarios
- CI/CD integration for automated testing
- Well-documented codebase with examples

### Library Support

- Expose a Go API (`client/` and `project/`) for programmatic use
- Enable embedding in other tools and workflows
- **API is unstable** - will change without notice until this message is gone

## Quick Start

### Prerequisites

```bash
# Add OCI image remotes to Incus
incus remote add --protocol oci docker.io https://docker.io
incus remote add --protocol oci ghcr.io https://ghcr.io
```

### Usage

```bash
# Build incus-compose
just build

# Create a compose.yaml
cat > compose.yaml <<EOF
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
EOF

# Start services
./bin/incus-compose up

# Check status
./bin/incus-compose ps

# Stop and remove
./bin/incus-compose down
```

See [Getting Started](docs/getting-started.md) for detailed examples.

## Credits

This project builds on work by [@bketelsen](https://github.com/bketelsen).
Some components are adapted from [docker compose](https://github.com/docker/compose).
AI tools assist with test generation, code reviews and docs; core implementation is hand-written.
