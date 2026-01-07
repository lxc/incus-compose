# Compose Compatibility

incus-compose implements a subset of the Compose Specification. This doc lists what works and what doesn't.

## Supported Features

### Services

- `image` - OCI images from any registry
- `command` - Override container command
- `working_dir` - Set working directory
- `environment` - Environment variables
- `labels` - Metadata (stored as `user.*` config)
- `depends_on` - Service dependency order
- `networks` - Multiple networks per service
- `ports` - Port publishing
- `volumes` - Named volumes and bind mounts
- `deploy.replicas` - Service scaling (instances named `{service}-{index}`)
- `restart` - Restart policies (`no`, `always`, `on-failure`, `unless-stopped`)

### Networks

- Bridge networks (Incus default)
- Network isolation between services
- DNS resolution by service name
- External networks (pre-existing Incus networks)

Not supported:

- Custom network drivers

### Volumes

- Named volumes (Incus custom storage volumes)
- Bind mounts (local connections only)
- Read-only volumes
- Automatic UID/GID shifting
- tmpfs mounts (with optional size limit)

Not supported:

- Volume driver options

### Environment

- `.env` file loading
- `env_file` directive
- Variable interpolation
- Default values: `${VAR:-default}`
- Required variables: `${VAR?error message}`

### Project

- `name` - Project name
- Project isolation (Incus projects)
- Profiles - Compose profiles

## Not Supported (Yet)

### Build

Docker-style builds are not supported:

```yaml
# Not supported
services:
  app:
    build: .
```

**Workaround:** Build images separately and push to a registry:

```bash
docker build -t ghcr.io/myorg/myapp:latest .
docker push ghcr.io/myorg/myapp:latest
```

Then reference in compose:

```yaml
services:
  app:
    image: ghcr.io/myorg/myapp:latest
```

### Health Checks

```yaml
# Not implemented
services:
  app:
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost"]
```

**Workaround:** Use Incus instance state monitoring or external health checks.

### Resource Limits

```yaml
# Not implemented
services:
  app:
    deploy:
      resources:
        limits:
          cpus: "0.5"
          memory: 512M
```

**Workaround:** Set Incus instance limits directly:

```bash
incus config set myapp-app limits.cpu 1
incus config set myapp-app limits.memory 512MiB
```

### Restart Policies

Restart policies are supported and map to Incus boot configuration:

| Compose `restart` | Incus Config                                   |
| ----------------- | ---------------------------------------------- |
| `no` (default)    | `boot.autostart=false`                         |
| `always`          | `boot.autostart=true`                          |
| `on-failure`      | `boot.autostart=true`, `boot.autorestart=true` |
| `unless-stopped`  | Uses last-state behavior (Incus default)       |

```yaml
services:
  app:
    image: docker.io/nginx:alpine
    restart: always
```

### Secrets

- `secrets` - File-based secrets pushed into container at `/run/secrets/{name}`
- `secrets[].file` - Read secret from file
- `secrets[].environment` - Read secret from environment variable
- Service `secrets[].target` - Custom target path
- Service `secrets[].uid` / `secrets[].gid` - File ownership
- Service `secrets[].mode` - File permissions (default: 0400)

Not supported:

- External secrets

### Configs

```yaml
# Not supported
configs:
  my_config:
    file: ./config.txt
```

**Workaround:** Use bind mounts or secrets.

### Extended Features

Not supported:

- `extends` - Service extension
- `deploy` - Most deployment options (except `replicas`)
- `links` - Legacy linking (use networks)
- `external_links` - Cross-project links

## Behavioral Differences

### Images

**Registry prefix required:**

Docker allows short image names, incus-compose requires the registry:

```yaml
# Docker Compose
image: nginx:alpine

# incus-compose
image: docker.io/nginx:alpine
```

Registries must be configured as Incus remotes first:

```bash
incus remote add --protocol oci docker.io https://docker.io
incus remote add --protocol oci ghcr.io https://ghcr.io
```

**Global cache:**

Like Docker, images are cached globally. An image pulled for one project is available to all projects. This avoids duplicate downloads.

**Registry authentication:**

Docker uses `~/.docker/config.json`. Incus uses remote configuration:

```bash
incus remote add --protocol oci docker.io https://docker.io --auth-type bearer
```

See [Incus documentation](https://linuxcontainers.org/incus/docs/main/howto/images_remote/) for details.

**Platform selection:**

Docker allows `--platform linux/amd64`. incus-compose uses the host architecture automatically. Multi-arch images select the correct variant.

### Port Publishing

**Docker Compose:**

```yaml
ports:
  - "8080:80" # iptables NAT rule
```

**incus-compose:**

```yaml
ports:
  - "8080:80" # Incus proxy device
```

Both work the same from outside, but Incus proxies are more efficient.

### Network Naming

**Docker Compose:**

```
{project}_{network}  # e.g., myapp_frontend
```

**incus-compose:**

```
{project}-{network}  # e.g., myapp-frontend (if ≤13 chars)
ic-{hash}            # e.g., ic-a1b2c3d4e5 (if >13 chars)
```

Network names are limited to 13 chars for dhclient compatibility.

### Volume Permissions

**Docker Compose:**

- Volumes owned by root by default
- Manual chown often needed

**incus-compose:**

- Volumes automatically shifted to match container's UID/GID
- Reads `oci.uid` and `oci.gid` from image
- Files appear with correct ownership inside container

### Instance Naming

Instances are named `{service}-{index}` where index starts at 1:

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    deploy:
      replicas: 3
```

Creates instances: `web-1`, `web-2`, `web-3`

You can also override replicas via CLI:

```bash
incus-compose up --scale web=5
```

### Environment Variables

**Docker Compose:**

```bash
export MY_VAR=value
docker-compose up  # MY_VAR available
```

**incus-compose:**

```bash
export MY_VAR=value
incus-compose up  # MY_VAR NOT available (security)
```

Use `.env` files or `--os-env` flag for docker-compose compatibility.

## Testing Compatibility

To test if your compose file works:

```bash
# Validate syntax
incus-compose config --quiet

# Show what will be created
incus-compose config

# Try starting
incus-compose up --no-start

# Check what was created
incus-compose list
```

## Reporting Compatibility Issues

If you find a compose feature that should work but doesn't, please report it with:

1. Minimal `compose.yaml` that reproduces the issue
2. Expected behavior (what docker-compose does)
3. Actual behavior (what incus-compose does)
4. Incus version: `incus version`
