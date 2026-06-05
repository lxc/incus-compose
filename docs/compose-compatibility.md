# Compose Compatibility

incus-compose implements a subset of the Compose Specification. This doc lists what works and what doesn't.

## Supported Features

### Incus Override File

If a `compose.incus.yaml` file exists next to the selected `compose.yaml`, incus-compose loads it automatically as an additional Compose file. Use it for Incus-specific overrides while keeping the upstream Docker Compose file unchanged.

```text
compose.yaml
compose.incus.yaml
```

Example `compose.incus.yaml`:

```yaml
services:
  web:
    ports: !reset []
    x-incus:
      limits.memory: 512MB

networks:
  default:
    x-incus:
      ipv4.address: 10.100.0.1/24
```

Running with the base file also applies the Incus override when present:

```bash
incus-compose -f compose.yaml up
```

The override file follows normal Compose merge rules. For example, `!reset []` clears a list from the base file.

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
- `x-incus` extension — pass any Incus project, network and instance option directly (see below)
- Top-level `x-incus-compose.network-profile` — reuse NIC devices from an existing Incus profile

#### x-incus Instance Extensions

Any Incus instance config key can be set via the `x-incus` extension block on a service definition. Keys are passed verbatim to the Incus instance config on creation.

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    x-incus:
      limits.memory: 512MB
      limits.cpu: "2"
```

Any [Incus instance option](https://linuxcontainers.org/incus/docs/main/reference/instance_options/) is accepted.

### Projects

```yaml
x-incus:
  limits.cpu: "4"
  limits.memory: 2049MB # +1 MiB
  limits.virtual-machines: 0

services:
  web:
    image: docker.io/nginx:alpine
    deploy:
      replicas: 4
    x-incus:
      limits.cpu: "1"
      limits.memory: 512MB
```

Any [Project option](https://linuxcontainers.org/incus/docs/main/reference/projects/) is accepted.

#### x-incus-compose Network Profile

Set top-level `x-incus-compose.network-profile` to copy NIC devices from an existing Incus profile into the project-local `default` profile:

```yaml
x-incus-compose:
  network-profile: default:default

services:
  web:
    image: docker.io/nginx:alpine
```

The value must use `{project}:{profile}` syntax. For example, `default:default` reads the `default` profile from the Incus `default` project.

When this option is set, incus-compose does not create compose-managed Incus network resources for service network attachments. Instances use the network devices provided by the copied profile instead. Service-level static IP assignments (`ipv4_address` / `ipv6_address`) are not supported in this mode because incus-compose does not create explicit NIC devices.

### Networks

- Bridge networks (Incus default)
- Network isolation between services
- DNS resolution by service name and by instance name
- External networks (pre-existing Incus networks)
- `x-incus` extension — pass any Incus network config key directly (see below)
- Automatic DHCP range configuration on creation (see below)
- Static IP assignment per service via `ipv4_address` / `ipv6_address` (see below)

Not supported:

- Custom network drivers

#### x-incus Network Extensions

Any Incus network config key can be set via the `x-incus` extension block on a network definition. Keys are passed verbatim to the Incus network config on creation.

```yaml
networks:
  backend:
    x-incus:
      ipv4.address: 10.100.0.1/24
      ipv6.address: fd42:abc::1/64
      ipv4.dhcp.ranges: 10.100.0.100-10.100.0.200
```

Any [Incus bridge network option](https://linuxcontainers.org/incus/docs/main/reference/network_bridge/) is accepted.

#### External Networks

Mark a network as `external: true` to attach services to a pre-existing Incus network.
incus-compose will never create or delete an external network.

```yaml
networks:
  shared:
    external: true
```

**Name resolution** — incus-compose probes the following candidates in order and uses
the first one that exists in Incus:

1. `x-incus-compose.network` value — raw (literal)
2. `x-incus-compose.network` value — sanitized (`{project}-{name}` / hash)
3. Compose network name — raw
4. Compose network name — sanitized

Use `x-incus-compose.network` when the Incus network name does not follow the compose
naming convention:

```yaml
networks:
  frontend:
    external: true
    x-incus-compose:
      network: my-production-net   # tried as-is first, then sanitized
```

If none of the candidates match an existing network, `up` fails with a not-found error.

#### Automatic DHCP Ranges

When a managed bridge network is created, incus-compose automatically configures DHCP ranges if they are not already set:

**IPv4** — The first quarter of the address block is reserved for static assignment. The DHCP range starts at that boundary:

| Subnet | Static range   | DHCP range       |
| ------ | -------------- | ---------------- |
| /24    | `.1–.63`       | `.64–.254`       |
| /16    | `.0.0–.63.255` | `.64.0–.255.254` |
| /28    | `.1–.3`        | `.4–.14`         |

**IPv6** — The first 256 addresses (`::0–::ff`) are reserved for static; DHCP runs from `::100` to `::ffff`. Stateful DHCPv6 (`ipv6.dhcp.stateful`) is enabled automatically.

Setting `ipv4.dhcp.ranges` or `ipv6.dhcp.ranges` in `x-incus` disables auto-calculation for that protocol. Existing networks (already present in Incus when `up` runs) are never modified.

#### Static IP Assignment

A service can be assigned a fixed IP on a specific network using the standard Compose
`ipv4_address` / `ipv6_address` fields on the per-service network attachment:

```yaml
services:
  db:
    image: docker.io/postgres:16-alpine
    networks:
      backend:
        ipv4_address: 10.100.0.10

  web:
    image: docker.io/nginx:alpine
    networks:
      backend:
        ipv4_address: 10.100.0.11
        ipv6_address: fd42:abc::11

networks:
  backend:
    x-incus:
      ipv4.address: 10.100.0.1/24
      ipv6.address: fd42:abc::1/64
```

The address is set as `ipv4.address` / `ipv6.address` on the Incus NIC device. The bridge's
built-in DHCP server reserves it so the instance always receives that address on the network.

The address must fall within the static zone (first quarter of the block) to avoid conflicts
with DHCP-assigned addresses.

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

Supported via the `ic-healthd` sidecar. See [Health Checking](healthd.md) for full details,
including config keys, defaults, security model, and `healthd` management commands.

The healthcheck status (`starting`, `healthy`, `unhealthy`) is reported in the `Status` column of
`incus-compose list` and `incus-compose ps` when healthchecks are configured.

Not yet supported:

- `start_period` - Grace period before checks start
- `start_interval` - Interval during start period
- `HEALTHCHECK` from Dockerfiles — see [healthd.md](healthd.md#dockerfile-healthcheck-not-supported)

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

Both work the same from outside. By default incus-compose uses userspace proxy devices (a Go
process per forwarded connection). For high-throughput services you can opt in to kernel-mode NAT
via a service extension, which installs nftables DNAT rules instead:

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
    networks:
      - frontend
    x-incus-compose:
      nat-proxy:
        - port: 8080 # listen port (matches the published port above)
          connect: 80 # container port to forward to
        - port: 8443
          connect: 443
          listen: # optional: restrict listen IPs (default: all bridge IPs)
            - 192.168.1.1
```

Each `nat-proxy` entry maps one published port to a container port. `listen` is optional; when
omitted, incus-compose discovers the bridge IP(s) from the attached network and listens on all of
them.

Requirements for `nat-proxy`:

- The service must be attached to at least one managed bridge network (a plain `networks:` entry).
- If no managed NIC is present, incus-compose falls back to userspace and logs a warning.
- After a manual `incus restart` the nftables rule may become stale; use `incus-compose up` to
  reapply.

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

### DNS Resolution

After `up`, both the **service name** and the **instance name** resolve inside containers:

```
database    → round-robins across all database instances (A/AAAA records)
database-1  → specific instance (registered by Incus dnsmasq)
```

This matches Docker Compose behavior. No configuration is required — records are
written automatically to the project bridge network's `raw.dnsmasq` and updated
whenever the scale changes.

**Note:** Setting `raw.dnsmasq` on the bridge disables AppArmor for the dnsmasq
process (not for containers). dnsmasq still runs as an unprivileged user.

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
