# Health Checking (ic-healthd)

incus-compose implements health checks via a sidecar container called `ic-healthd`.
Incus has no native healthcheck support, so ic-healthd fills that role.

## How It Works

When `incus-compose up` finds services with a `healthcheck` directive, it:

1. Resolves the Incus bridge healthd should attach to (see [Network Configuration](#network-configuration)).
2. Creates a restricted Incus trust token scoped to the project.
3. Starts an `ic-healthd` sidecar container, attaches it to the bridge, and injects the token as a secret.
4. ic-healthd authenticates once (token consumed), persists the resulting cert.
5. ic-healthd discovers which instances to watch by reading the Incus API.
6. ic-healthd runs the health loop and updates instance config keys with the result.

The sidecar starts after all regular instances (priority = `PriorityInstance + 1`)
and is removed when `incus-compose down` runs.

## Config Storage

Health check config and runtime state live in the instance's `user.*` config keys.
There is no separate config file. ic-healthd reads these keys at startup and on
every check cycle via `GetInstancesFull`.

```
user.healthcheck.test      '["CMD","wget","-q","--spider","http://localhost"]'
user.healthcheck.interval  10s
user.healthcheck.timeout   5s
user.healthcheck.retries   3
user.healthcheck.status    healthy | unhealthy | starting
user.restart               always | on-failure
```

These keys are visible in `incus config show <instance>`:

```
user.healthcheck.interval: 10s
user.healthcheck.retries: "3"
user.healthcheck.status: unhealthy
user.healthcheck.test: '["CMD","wget","-q","--spider","http://localhost"]'
user.healthcheck.timeout: 5s
user.restart: always
```

`user.healthcheck.status` is the only key that ic-healthd writes back; all others
are set by incus-compose at instance creation time and treated as read-only by
the daemon.

## Defaults

When keys are missing, ic-healthd falls back to:

| Key      | Default |
| -------- | ------- |
| interval | 5s      |
| timeout  | 5s      |
| retries  | 3       |

## Dockerfile HEALTHCHECK Not Supported

incus-compose does not read or inherit the `HEALTHCHECK` instruction embedded in Docker images.

Incus imports OCI images via umoci, which converts the OCI image config into an OCI runtime spec.
The Docker `HEALTHCHECK` extension is not part of the OCI image spec and is discarded during that
conversion — by the time the image is cached in Incus, the healthcheck data no longer exists.

Fetching it directly from the registry at `up` time would require registry access on every run
and fails in air-gapped environments.

**Workaround:** Always declare `healthcheck.test` explicitly in the compose file:

```yaml
services:
  db:
    image: docker.io/postgres:16-alpine
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "postgres"]
      interval: 10s
      timeout: 5s
      retries: 5
```

## Restart Without a Test

`restart: always` or `restart: on-failure` without a `healthcheck` block is also
handled. ic-healthd monitors the instance state and restarts it when stopped,
without running an exec-based test command.

## Network Configuration

ic-healthd needs an Incus bridge for its NIC device and uses that bridge's gateway
IP to reach the Incus HTTPS API (`:8443`).

### Auto-detection

When no network is specified, incus-compose probes in order:

1. **`incusbr0`** — the default Incus bridge, present on most installations
2. **`eth0` of the `default` profile** — reads the network name from the profile's `eth0` device
3. **First compose-managed network** — falls back to the first network defined in the compose file

### Explicit override

Set via CLI flag, environment variable, or compose-file extension.
CLI/env takes priority over the compose file.

```bash
incus-compose up --healthd-network incusbr0
# or
INCUS_COMPOSE_HEALTHD_NETWORK=incusbr0 incus-compose up
```

```yaml
x-incus-compose:
  healthd-network: incusbr0
```

When set explicitly, the named network must exist — incus-compose errors out if not found.

The same flag is available on `incus-compose healthd up --network`.

## Security

The restricted token gives ic-healthd project-scoped access only:

- Can exec commands into instances in the project.
- Can manage instance state (start/stop/restart) within the project.
- Cannot access other projects or perform global operations.

## Management Commands

The `healthd` command group lets you manage the sidecar directly without touching services:

```
incus-compose healthd logs [--follow]
incus-compose healthd reload
incus-compose healthd restart
incus-compose healthd up [--recreate]
incus-compose healthd down
```

| Subcommand        | Description                                           |
| ----------------- | ----------------------------------------------------- |
| `logs [--follow]` | Stream the ic-healthd container log                   |
| `reload`          | Send SIGHUP to the ic-healthd process (reload config) |
| `restart`         | Restart the ic-healthd container                      |
| `up [--recreate]` | Create or recreate the sidecar                        |
| `down`            | Stop and remove the sidecar                           |

`healthd up` accepts `--image`, `--binary`, and `--network`.
`healthd up` refuses with an error when no service in the project declares a `healthcheck`.

## Disabling the Sidecar

```bash
incus-compose up --no-healthd
```

## Development: Local Binary

```bash
incus-compose up --healthd-binary ./bin/ic-healthd
```

Uses `images:alpine/edge` instead of the published OCI image and pushes the
local binary into the container before start. Useful when iterating on ic-healthd
itself.

## Sidecar Image

Default image: `registry.gitlab.com/r3j0/incus-compose/ic-healthd:{version}`

Override with `--healthd-image` flag or `INCUS_COMPOSE_HEALTHD_IMAGE` env var.

The container is named `ic-healthd` within the project and tagged with
`user.healthcheck.daemon=true` so ic-healthd skips itself during discovery.

## Troubleshooting

**Sidecar has wrong config (missing `--incus`/`--project` flags)?**

This can happen when ic-healthd was created by an older version of incus-compose.
Recreate it:

```bash
incus-compose healthd up --recreate
```

**Sidecar not running after `incus-compose start`?**

Healthd is only included in `start` if the project has services with a `healthcheck`.
Use `incus-compose healthd up` to start it independently.
