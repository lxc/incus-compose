# Architecture

High-level architecture of incus-compose and how components fit together, a **resource-first design**:

- **Unified Resource Interface** - Images, instances, networks, profiles, and volumes are all first-class resources
- **Two-Phase Pattern** - Configuration (resource creation) then execution (ensure/start/stop/delete)
- **Priority-Based Ordering** - Dependencies managed via numeric priorities, no complex graph resolution
- **Stack Execution** - Batch operations with parallel image downloads
- **Hook System** - Before/after action interception for logging and validation

## Package Structure

```
incus-compose/
├── cmd/incus-compose/  # CLI entry point
├── client/             # Incus client with resources, stack, pool
└── project/            # Compose-spec to Incus translation
```

### Package Responsibilities

**cmd/incus-compose/**

- CLI and flag parsing
- Wires together client and project
- Commands: up, down, ps, config

**client/**

- High-level Incus API wrapper
- Resources: Profile, Image, Network, StorageVolume, Instance
- Stack for task collection and ordering
- WorkerPool for parallel execution
- Hooks for action interception

**project/**

- Loads Docker Compose files via compose-go
- Translates compose services to Incus resources
- Configures client resources based on compose definitions
- Handles environment variables and dependencies

### Package Dependencies

```
cmd/incus-compose
    ├── client   (creates GlobalClient, runs Stack)
    └── project  (loads compose, configures client resources)

project
    └── client   (calls client.Resource() to create resources)
```

The CLI creates a GlobalClient and loads the compose project. Then project takes over:
it reads the compose definitions and configures resources on the client. The client
owns the resources, but project drives what gets created.

This means project is not a passive loader. It actively builds the resource graph
by calling into client. The Stack returned by project contains all resources ready
for execution.

## Resource Hierarchy

```
GlobalClient
  ├── imageCache (default project, configurable via INCUS_COMPOSE_IMAGE_CACHE)
  └── Client (project-scoped)
        ├── Profile
        ├── Image
        ├── Network
        ├── StorageVolume
        └── Instance
              ├── Devices (pre-creation)
              └── PostDevices (post-creation)
```

## Image Caching (2-Stage Flow)

Images go through two stages:

1. **Remote** - OCI registry (docker.io, ghcr.io)
2. **Cache** - Incus `default` project (configurable via `INCUS_COMPOSE_IMAGE_CACHE`)

```
Registry ──pull──> Cache ──use──> Instance
           (slow)
```

Benefits:

- First pull is slow (network), subsequent runs are fast (local cache)
- No registry rate limits after initial download
- Cache persists across `down`/`up` cycles
- Project deletion does not affect the cache

## Two-Phase Resource Pattern

1. **Configuration phase** - Resource created in memory

   ```go
   image, _ := client.Resource(KindImage, "docker.io/alpine", &ImageConfig{})
   image.Config.Source = imageServer  // configure
   ```

2. **Execution phase** - Resource created on Incus
   ```go
   image.Ensure(OptionCreate())  // blocks, creates on server
   ```

## Stack, WorkerPool, and Hooks

See [Client Package](architecture/client/README.md) for Stack, WorkerPool, resource ordering, and hook details.

## Name Sanitization

### Projects

`My_Project!` -> `my-project`

### Instances

Valid DNS names, max 63 chars, long names hashed to 32 hex chars.

### Networks

Linux interface limit (13 chars), uses hash for long names:
`backend` -> `app-backend` or `ic-a1b2c3d4e5`

## Error Handling

See [Errors](architecture/client/errors.md) for sentinel errors and context enrichment.

## Connection Modes

**Direct URL (testing/CI):**

```bash
export INCUS_COMPOSE_URL="https://192.168.1.100:8443"
export INCUS_COMPOSE_CERT="./certs/client.crt"
export INCUS_COMPOSE_KEY="./certs/client.key"
```

**Provided connection (for testing):**

```go
client.New(ctx, client.ClientProvideConnection(instanceServer, cacheServer))
```

## Environment Variables

- OS environment variables NOT included by default
- `.env` files can use OS variables for interpolation
- Use `--os-env` flag for Docker Compose compatibility

## Extensions

### x-incus (Raw Incus Options)

Pass raw Incus configuration options directly to instances and networks:

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    x-incus:
      limits.memory: 512MB
      limits.cpu: "2"
      security.nesting: "false"

networks:
  custom:
    x-incus:
      nat: "false"
      ipv4.nat: "true"
```

All key-value pairs are passed verbatim to Incus. See the [Incus instance options reference](https://linuxcontainers.org/incus/docs/main/reference/instance_options/) for available options.

### x-incus-compose (Compose-Specific Features)

Compose-specific transformations and conveniences handled by incus-compose:

```yaml
x-incus-compose:
  network:
    project: default
    profile: default

services:
  app:
    image: docker.io/myapp:latest
```

`network-profile` copies NIC devices from an existing Incus profile into the project-local `default` profile. The value uses `{project}:{profile}` syntax. When this option is set, incus-compose does not create compose-managed network resources for service network attachments; instances use networking from the copied profile instead.

Service-level static IP assignments are rejected with `network-profile` because incus-compose does not create an explicit NIC device in that mode.

## Quick Reference

### Common Commands

```bash
incus-compose up                   # Start services
incus-compose up --no-start        # Create without starting
incus-compose up --recreate        # Recreate existing containers
incus-compose down                 # Stop and remove
incus-compose down --volumes       # Also remove volumes
incus-compose list                 # List running containers
incus-compose config --quiet       # Validate compose file
incus-compose config               # Show resolved configuration
incus-compose config --services    # List service names
incus-compose config --networks    # List network names
incus-compose config --volumes     # List volume names
incus-compose config --environment # Show interpolation environment
```

### Common Patterns

```yaml
# Basic service
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"

# With dependencies
services:
  db:
    image: docker.io/postgres:16-alpine
  app:
    image: docker.io/myapp:latest
    depends_on:
      - db

# With named volume
services:
  app:
    image: docker.io/myapp:latest
    volumes:
      - data:/var/lib/app
      - ./config:/etc/app:ro
volumes:
  data:

# With environment file
services:
  app:
    image: docker.io/myapp:latest
    environment:
      DATABASE_URL: ${DATABASE_URL}
    env_file:
      - .env
```

## Documentation

See the [docs index](README.md) for all user and contributor docs. Closely related:

- [Client Package](architecture/client/README.md) - Resources, Stack, WorkerPool
- [Testing](architecture/testing.md) - Testing patterns and fixtures
- [Health Checking](healthd.md) - ic-healthd sidecar

## Need Help?

- **Bugs/Features**: Open an issue on GitLab
- **Questions**: Check the docs above or open a discussion
