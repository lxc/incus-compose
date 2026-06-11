# Health Checking (ic-healthd)

incus-compose implements health checks via a sidecar container called `ic-healthd`.
Incus has no native healthcheck support, so ic-healthd fills that role.

## How It Works

`incus-compose up` creates the sidecar when any service declares a `healthcheck`,
has a restart policy other than `no`, or is depended on with `condition: service_healthy`.
It then:

1. Resolves the Incus bridge healthd should attach to (see [Network Configuration](#network-configuration)).
2. Creates a restricted Incus trust token scoped to the project.
3. Starts the `ic-healthd` sidecar, attaches it to the bridge, and injects the token as a secret.
4. ic-healthd authenticates once (token consumed) and persists the resulting cert.
5. ic-healthd discovers which instances to watch by reading the Incus API.
6. ic-healthd runs the health loop and writes the result to `user.healthcheck.status`.

The sidecar starts before the regular services so `service_healthy` dependencies
can be evaluated, and is removed by `incus-compose down`.

## Config Storage

Health check config and runtime state live in the instance's `user.*` config keys.
There is no separate config file. ic-healthd reads these keys at startup and on
SIGHUP (`incus-compose healthd reload`).

See the Docker healthcheck docs for the value semantics: https://docs.docker.com/reference/dockerfile#healthcheck

```
user.healthcheck.test            '["CMD","wget","-q","--spider","http://localhost"]'
user.healthcheck.start_period    10s
user.healthcheck.start_interval  2s
user.healthcheck.interval        10s
user.healthcheck.timeout         5s
user.healthcheck.retries         3
user.healthcheck.status          starting | healthy | unhealthy
user.restart                     always | on-failure | unless-stopped
```

These keys are visible in `incus config show <instance>`.

`user.healthcheck.status` is the only key ic-healthd writes back; all others are
set by incus-compose at instance creation time and treated as read-only by the
daemon. incus-compose sets the initial status to `starting`.

## Defaults

When keys are missing, ic-healthd falls back to:

| Key            | Default       |
| -------------- | ------------- |
| start_period   | 0s (disabled) |
| start_interval | 5s            |
| interval       | 30s           |
| timeout        | 30s           |
| retries        | 3             |

`retries` must be greater than 0.

After `retries` consecutive failures the instance is restarted. The first
restart waits `interval * retries`; the delay doubles on every further restart,
capped at 60s.

## Dockerfile HEALTHCHECK Not Supported

incus-compose does not read or inherit the `HEALTHCHECK` instruction embedded in Docker images.

Incus imports OCI images via umoci, which converts the OCI image config into an
OCI runtime spec. The Docker `HEALTHCHECK` extension is not part of the OCI image
spec and is discarded during that conversion. Fetching it from the registry at
`up` time would require registry access on every run and fails in air-gapped
environments.

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

`restart: always`, `on-failure`, or `unless-stopped` without a `healthcheck`
block is also handled. ic-healthd monitors the instance state and restarts it
when stopped, without running an exec-based test command.

With `unless-stopped`, instances stopped intentionally (`user.healthcheck.stopped=true`,
set by `incus-compose stop`) are not restarted.

## Network Configuration

ic-healthd needs an Incus bridge for its NIC device and uses that bridge's gateway
IP to reach the Incus HTTPS API (`:8443`).

### Auto-detection

When no network is specified, incus-compose probes in order:

1. **`incusbr0`** - the default Incus bridge, present on most installations
2. **The bridge of the current connection** - the network whose gateway IP matches
   the IP incus-compose itself uses to reach the Incus API

If neither matches, `up` fails; set the network explicitly.

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

When set explicitly, the named network must exist - incus-compose errors out if not found.

The same flag is available on `incus-compose healthd up --network`.

## Security

The restricted token gives ic-healthd project-scoped access only:

- Can exec commands into instances in the project.
- Can manage instance state (start/stop/restart) within the project.
- Cannot access other projects or perform global operations.

## Management Commands

The `healthd` command group manages the sidecar directly without touching services:

| Subcommand        | Description                                           |
| ----------------- | ----------------------------------------------------- |
| `logs [--follow]` | Stream the ic-healthd container log                   |
| `reload`          | Send SIGHUP to the ic-healthd process (reload config) |
| `restart`         | Restart the ic-healthd container                      |
| `up [--recreate]` | Create or recreate the sidecar                        |
| `down`            | Stop and remove the sidecar                           |

`healthd up` accepts `--image`, `--binary`, and `--network`. It refuses with an
error when no service in the project requires healthd (no healthcheck, no restart
policy, no `service_healthy` dependency).

Healthd debug logging is controlled by the global incus-compose `--debug` flag,
which is inherited by healthd operations.
Use `incus-compose --debug healthd up --recreate` to enable debug logs;
omit `--debug` to keep normal log verbosity.

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

The container is named `{project}-ic-healthd` and tagged with
`user.healthcheck.daemon=true` so ic-healthd skips itself during discovery.

## Troubleshooting

**Sidecar has wrong config (missing `--incus`/`--project` flags)?**

This can happen when ic-healthd was created by an older version of incus-compose.
Recreate it:

```bash
incus-compose healthd up --recreate
```

**Sidecar not running after `incus-compose start`?**

`start` never creates or starts the sidecar; only `up` does. Use
`incus-compose healthd up` to start it independently.
