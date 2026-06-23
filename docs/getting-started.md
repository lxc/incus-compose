# Getting Started

incus-compose lets you run your existing `compose.yaml` files directly on Incus without Docker.

## Prerequisites

- Incus 6.3+ installed and running
- Access to an Incus server (local or remote)
- `podman` or `docker` for image building (see [Builds](build.md))

### HTTPS Remote

Switch to a https remote (required for healthchecking).

incus-compose auto-detects the bridge healthd should use by the default profile's eth0.
Use `--network-project` and `--network-profile` if your setup differs — see [Network Configuration](healthd.md#network-configuration).

1.) Get the IP address of your main bridge (incusbr0 or the one in the default profile).

```bash
incus network list
```

2.) Either set that IP as `$IP:8443` or listen on all interfaces with `:8443`

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

### OCI Image Remotes

Add OCI image remotes to Incus, read [OCI Registry Cache](../oci-registry-cache/README.md) first as you wish.

```bash
incus remote add --protocol oci docker.io https://docker.io
incus remote add --protocol oci ghcr.io https://ghcr.io
incus remote add --protocol oci registry.gitlab.com https://registry.gitlab.com
```

## Installation

### Install script (recommended)

The install script downloads the matching release for your OS/architecture and
verifies it against the published SHA-256 checksums.

```bash
# Into a user-writable directory on your PATH (no sudo and self-update working)
curl -sSfL https://raw.githubusercontent.com/lxc/incus-compose/main/install.sh | sh -s -- -b ~/.local/bin

# Or System-wide into /usr/local/bin
curl -sSfL https://raw.githubusercontent.com/lxc/incus-compose/main/install.sh | sudo sh -s -- -b /usr/local/bin
```

Pass a release tag as the final argument to pin a version, e.g.
`... | sudo sh -s -- -b /usr/local/bin 1.0.0-beta15`. Without a tag the latest
release is installed.

### Binary

Download a prebuilt archive from the [Releases Page](https://github.com/lxc/incus-compose/releases).

### Source

```bash
# Build from source
git clone https://github.com/lxc/incus-compose
cd incus-compose
just build

# Or install directly
go install github.com/lxc/incus-compose/cmd/incus-compose@latest
```

## Quick Start

### 1. Create a compose.yaml

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
    volumes:
      - ./html:/usr/share/nginx/html:ro

  app:
    image: docker.io/node:20-alpine
    working_dir: /app
    volumes:
      - ./app:/app
    command: node server.js
    depends_on:
      - web
```

### 2. Start your services

```bash
incus-compose up
```

This will:

- Create an Incus project named after your directory
- Pull images if needed
- Create networks and volumes
- Start containers in dependency order

If your compose file uses health checks, incus-compose manages the `ic-healthd` sidecar automatically. It is transparent during normal use, but it is also a core component: all `healthcheck`, `restart:` and `depends_on: service_healthy` behavior is enforced by this sidecar, not by Incus. A working healthd is also required to bring up a project that has `service_healthy` dependencies - `up` waits for healthd to report them healthy, so a broken healthd makes `up` hang and fail (unless you pass `--no-healthd`). If health, restart, or startup behavior ever looks wrong, debug healthd first - see [Health Checking](healthd.md) and [Debugging ic-healthd](healthd.md#debugging-ic-healthd).

### 3. Check status

```bash
incus-compose list
```

### 4. View logs

```bash
# View logs from all services
incus-compose logs

# Follow logs in real-time
incus-compose logs -f

# View logs from specific services
incus-compose logs web app
```

### 5. Stop and remove

```bash
# Stop and remove containers
incus-compose down

# Also remove images used by the services
incus-compose down --images

# Remove the whole project, including volumes and images (--volumes is an alias)
incus-compose down --project
```

## compose.incus.yaml Override

`compose.incus.yaml` is loaded automatically when it exists next to the selected `compose.yaml`. This lets you keep an upstream or Docker-focused Compose file unchanged while adding Incus-specific settings in a separate file.

Typical uses:

- Remove Docker-only port publishing with `ports: !reset []`
- Add explicit health checks for `ic-healthd`
- Set static service IPs on Incus networks
- Pass raw Incus network or instance options via `x-incus`

Example `compose.incus.yaml`:

```yaml
services:
  web:
    ports: !reset []
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost"]
    networks:
      default:
        ipv4_address: 10.131.32.17

networks:
  default:
    x-incus:
      ipv4.nat: "true"
      ipv4.address: 10.131.32.1/24
```

The file follows normal [Compose merge rules](https://docs.docker.com/reference/compose-file/merge). For example, `!reset []` clears a list from the base file. See [Compose Compatibility](compose-compatibility.md#incus-override-file) for details.

## Common Workflows

### Multi-service application

```yaml
services:
  db:
    image: docker.io/postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: dev123
    volumes:
      - pgdata:/var/lib/postgresql/data

  api:
    image: docker.io/myapp/api:latest
    depends_on:
      - db
    environment:
      DATABASE_URL: postgres://postgres:dev123@db/myapp

  web:
    image: docker.io/myapp/frontend:latest
    depends_on:
      - api
    ports:
      - "3000:80"

volumes:
  pgdata:
```

Services start in order: db → api → web

### Using environment files

```env
# .env
DB_PASSWORD=secret123
API_PORT=3000
```

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
```

Only variables defined in `.env` are available (not your shell environment).

## Key Differences from Docker Compose

### Real IP Addresses

Incus gives each container a real IP on your network:

```bash
$ incus-compose list
KIND      NAME                    INCUSNAME                       IMAGE                           STATUS   ADDRESSES
image     docker.io/nginx:alpine  docker.io/library/nginx:alpine                                  Exists
network   default                 ic-ynmt73wxwq                                                   Exists
instance  web-1                   web-1                           docker.io/library/nginx:alpine  Running  10.149.206.30
```

You can access containers directly: `curl http://10.149.206.30`

### Port Publishing

Published ports use Incus proxy devices (not iptables NAT):

```yaml
ports:
  - "8080:80" # Host 8080 → Container 80
```

### Volumes

Named volumes are Incus custom storage volumes with automatic UID/GID shifting:

```yaml
volumes:
  data:/app/data  # Named volume with proper permissions
  ./local:/app    # Bind mount (local connections only)
```

Bind mounts only work with local Incus (Unix socket). For remote Incus, use named volumes.

### Networks

Each network becomes an Incus bridge network with deterministic naming:

```yaml
networks:
  frontend:
  backend:
```

Long network names are hashed to fit Linux interface limits (13 chars for dhclient compatibility).

## Project Isolation

Each compose project gets its own Incus project:

```bash
$ incus-compose -p myapp up
# Creates Incus project "myapp"

$ incus-compose -p testing up
# Separate Incus project "testing"
```

Projects are isolated: separate networks, volumes, and instances.

## Image Caching

Images are cached in either the `default` project or project you set via the `INCUS_COMPOSE_IMAGE_CACHE` env:

```bash
$ incus project list
+---------------------------+--------+----------+-----------------+-----------------+----------+---------------+------------------------------------------+---------+
|           NAME            | IMAGES | PROFILES | STORAGE VOLUMES | STORAGE BUCKETS | NETWORKS | NETWORK ZONES |               DESCRIPTION                | USED BY |
+---------------------------+--------+----------+-----------------+-----------------+----------+---------------+------------------------------------------+---------+
| default (current)         | YES    | YES      | YES             | YES             | YES      | YES           | Default Incus project                    | 14      |
+---------------------------+--------+----------+-----------------+-----------------+----------+---------------+------------------------------------------+---------+
| immich                    | YES    | YES      | YES             | YES             | NO       | NO            | incus-compose: immich                    | 7       |
+---------------------------+--------+----------+-----------------+-----------------+----------+---------------+------------------------------------------+---------+
```

This means:

- First run pulls from registry (slow)
- Subsequent runs copy from local cache (fast)
- No Docker Hub rate limits after initial pull
- `incus-compose down` only removes project images, cache persists

For a technical background about images see [architecture/client/image.md](docs/architecture/client/image.md)

The cache project is created automatically on first use.

## Next Steps

- [CLI Reference](cli.md)
- [Builds](build.md)
- [Compose Compatibility](compose-compatibility.md) - What features are supported
- [Health Checking](healthd.md) - Healthchecks and restart policies
- [Environment Variables](environment-variables.md) - How env vars work
- [Why Incus?](why-incus.md) - Benefits over Docker
- [Docs Index](README.md) - All user and contributor docs
