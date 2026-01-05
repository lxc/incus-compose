# Getting Started

incus-compose lets you run your existing `compose.yaml` files directly on Incus without Docker.

## Prerequisites

- Incus 6.3+ installed and running
- Access to an Incus server (local or remote)

## Installation

```bash
# Build from source
just build

# The binary will be in ./incus-compose
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

### 3. Check status

```bash
incus-compose list
```

### 4. Stop and remove

```bash
# Stop and remove containers
incus-compose down

# Also remove volumes
incus-compose down --volumes

# Also remove networks
incus-compose down --volumes --networks
```

## Common Workflows

### Development with live code

```yaml
services:
  app:
    image: docker.io/python:3.11
    volumes:
      - ./src:/app
    working_dir: /app
    command: python -m http.server 8000
```

Edits to `./src` are immediately visible inside the container.

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
instance  web                     web                             docker.io/library/nginx:alpine  Running  10.149.206.30
```

You can access containers directly: `curl http://10.0.1.42`

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

Images are cached in a dedicated `incus-compose-images` project:

```bash
$ incus project list
+----------------------+--------+----------+-----------------+-----------------+
|         NAME         | IMAGES | PROFILES | STORAGE VOLUMES | STORAGE BUCKETS |
+----------------------+--------+----------+-----------------+-----------------+
| default              | YES    | YES      | YES             | YES             |
| incus-compose-images | YES    | YES      | YES             | YES             |
| myapp                | YES    | YES      | YES             | YES             |
+----------------------+--------+----------+-----------------+-----------------+
```

This means:

- First run pulls from registry (slow)
- Subsequent runs copy from local cache (fast)
- No Docker Hub rate limits after initial pull
- `incus-compose down` only removes project images, cache persists

The cache project is created automatically on first use.

## Next Steps

- [Compose Compatibility](compose-compatibility.md) - What features are supported
- [Environment Variables](environment-variables.md) - How env vars work
- [Why Incus?](why-incus.md) - Benefits over Docker
