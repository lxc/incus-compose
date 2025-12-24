# incus-compose

Bring the familiar Docker Compose workflow to Incus containers. `incus-compose` implements the Compose specification for the Incus ecosystem, allowing you to define and run multi-container applications using the same `docker-compose.yml` files you already know.

## Status

**Early Development** - This project is in its initial phase. APIs and behavior may change. Contributions and feedback are welcome!

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

- Expose a Go API (`pkg/icclient/`) for programmatic use
- Enable embedding in other tools and workflows
- **API is unstable** - will change without notice until this message is gone

## Usage

### docker.io and ghcr.io images

Simply add the remote as `docker.io` or `ghcr.io` to your incus server:

```sh
incus remote add --protocol oci docker.io https://docker.io
incus remote add --protocol oci ghcr.io https://ghcr.io
```

Now you can use `incus-compose` to pull and run images from those remotes, e.g.:

```yaml
services:
  hello-world:
    image: docker.io/hello-world:latest
```

## Credits

This is based on work done by [@bketelsen](https://github.com/bketelsen)
Some parts are replicated or copied from [docker compose](https://github.com/docker/compose).
Im using AI to generate tests and to help me with reviews, real code is 90% hand written.
