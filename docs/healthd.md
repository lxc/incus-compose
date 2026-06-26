# Health Checking (ic-healthd)

incus-compose implements health checks via a sidecar container called `ic-healthd`.
Incus has no native healthcheck support, so ic-healthd fills that role.

> **ic-healthd is a core component.** Every `healthcheck`, every restart policy
> (`restart: always | on-failure | unless-stopped`), and every
> `depends_on: { condition: service_healthy }` is enforced by this sidecar, not by
> Incus. If healthd is misconfigured, stopped, or crashing:
>
> - instances are not restarted, and
> - **the project may fail to come up at all**: `incus-compose up` waits for
>   `service_healthy` dependencies to be reported healthy by healthd. If that
>   status never arrives, `up` blocks until `--dependency-timeout` (default 5m;
>   `0` waits forever) and then fails.
>
> Opt out of healthd entirely with `incus-compose up --no-healthd` (this also
> drops the dependency wait); `--no-deps` skips the wait too. When health,
> restart, or startup-ordering behavior looks wrong, debug healthd first (see
> [Debugging ic-healthd](#debugging-ic-healthd)).

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
capped at 5 minutes.

### Retries during the start period

The `retries` value above applies to the normal checker only. During the start
period the instance is given the whole period to come up, so the start-period
checker derives its own retry budget from the period itself:

```
start retries = start_period / start_interval
```

That is the number of checks that fit in the start period. A check that succeeds
at any point ends the start period early and switches to the normal checker. If
the instance never becomes healthy, the start period elapses and the checker
either restarts the instance (when a restart policy is set) or falls back to the
normal checker.

Keep `start_interval` smaller than `start_period`: if it is larger, the derived
budget rounds down to zero and the instance is restarted on the first failed
check during start. `start_interval` must also be a positive, at-least-1ms
duration.

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

ic-healthd runs in its own container and must reach the Incus HTTPS API from the
inside. Two things are configured:

- **`network`** - the Incus network (or host bridge) healthd attaches its NIC to.
- **`incus`** - the Incus API URL healthd connects to.

Both can be set in the compose file or overridden on the CLI. CLI flags and
environment variables take priority over the compose file.

```yaml
name: my-project
x-incus-compose:
  healthd:
    # Incus API endpoint healthd connects to.
    # Default: the bridge IP of `network` below, with the port incus-compose
    # itself connected on.
    incus: https://:8443
    # `<project>:<network>` for a managed network, or a plain bridge name.
    # Default: the `default` network of the current project.
    network: default:default
```

| Flag                | Environment variable           | Compose key                       |
| ------------------- | ------------------------------ | --------------------------------- |
| `--healthd-incus`   | `INCUS_COMPOSE_HEALTHD_INCUS`   | `x-incus-compose.healthd.incus`   |
| `--healthd-network` | `INCUS_COMPOSE_HEALTHD_NETWORK` | `x-incus-compose.healthd.network` |

`incus-compose healthd up` takes the same two options as `--incus` and `--network`.

### `network`

- **Empty (default)** - the `default` network of the current project. incus-compose
  creates it if needed, so healthd can come up before the rest of the project.
- **`<project>:<network>`** - a managed Incus network, optionally in another
  project. It must already exist; incus-compose never creates it.
- **A value without `:`** - a host bridge name (e.g. `incusbr0`). It must already
  exist.

The network's IPv4 gateway is used as the default Incus endpoint, so healthd can
reach Incus over that bridge.

### `incus`

- **Empty (default)** - `https://<network gateway IP>:<client port>`. The port is
  the one incus-compose used for its own connection, so Incus must be listening on
  the bridge IP (commonly all interfaces, `core.https_address = :8443`). This
  requires a HTTPS connection; over a unix socket there is no port to reuse, so set
  `--healthd-incus` explicitly.
- **An explicit URL** - used verbatim, e.g. `https://10.0.0.1:8443`. Combine with
  `network` to pin both the bridge and the endpoint.

### Combinations

| `network`                    | `incus` | Behavior                                      |
| ---------------------------- | ------- | --------------------------------------------- |
| default                      | empty   | Project bridge IP + client port (the default) |
| default                      | URL     | Project bridge for the NIC, pinned endpoint   |
| bridge / `project:network`   | empty   | Different bridge, auto-detected IP            |
| bridge / `project:network`   | URL     | Different bridge, pinned endpoint             |

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

`healthd up` accepts `--image`, `--binary`, `--incus`, and `--network`. It refuses with an
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

Default image: `ghcr.io/lxc/incus-compose/ic-healthd:{version}`

Override with `--healthd-image` flag or `INCUS_COMPOSE_HEALTHD_IMAGE` env var.

The container is named `{project}-ic-healthd` and tagged with
`user.healthcheck.daemon=true` so ic-healthd skips itself during discovery.

## Debugging ic-healthd

Because healthd drives all health and restart behavior, most "container did not
restart" or "stuck `service_healthy`" problems are diagnosed from the sidecar.
Work through these in order.

### 1. Check the reported health status

Instances are named `<service>-1` (the replica index starts at 1) and live in the
Incus project named after your compose project, so pass `--project`. ic-healthd
writes its verdict to `user.healthcheck.status`
(`starting | healthy | unhealthy`):

```bash
incus config get web-1 user.healthcheck.status --project <project>
```

`starting` that never becomes `healthy` means the test never passes within the
start period; `unhealthy` means it failed `retries` times.

### 2. Inspect the config keys healthd reads

All inputs live in `user.healthcheck.*` (and `user.restart`). If a key is wrong,
healthd behaves wrong - it never reads the compose file directly:

```bash
incus config show web-1 --project <project> | grep -E 'user\.(healthcheck|restart)'
```

### 3. Watch the daemon logs

```bash
incus-compose healthd logs --follow
```

Enable debug logging for full per-check detail (failures, retry counts,
`inStart` transitions, restart delays). The `--debug` flag is inherited by the
sidecar, so recreate it with debug on:

```bash
incus-compose --debug healthd up --recreate
incus-compose healthd logs --follow
```

### 4. Confirm the sidecar is actually running

The container is named `{project}-ic-healthd`. If it is missing or stopped,
nothing is being monitored:

```bash
incus-compose list --healthd
incus-compose healthd up --recreate   # recreate if missing/stale
```

Remember: `incus-compose start` never (re)starts the sidecar - only `up` does.

### 5. Reproduce the health test by hand

healthd runs `user.healthcheck.test` via `incus exec`. Run it yourself to see
why it fails:

```bash
incus-compose exec <service> -- sh -c 'wget -q --spider http://localhost; echo exit: $?'
```

### 6. Reload after editing keys

If you change `user.healthcheck.*` keys directly (instead of via `up`), tell the
running daemon to re-read them:

```bash
incus-compose healthd reload   # sends SIGHUP
```

### `incus-compose up` hangs or times out on dependencies

If a service uses `depends_on: { condition: service_healthy }`, `up` waits for
healthd to report the dependency `healthy` before starting the dependent service.
A broken or missing healthd means that status never arrives and `up` blocks until
`--dependency-timeout` (default 5m) elapses, then fails.

1. Confirm the dependency's status with steps 1-3 above; it is likely stuck on
   `starting` or `unhealthy`.
2. If you only want to bring the project up without the wait, opt out:

   ```bash
   incus-compose up --no-healthd   # also stops managing healthchecks/restarts
   # or keep healthd but skip the wait:
   incus-compose up --no-deps
   ```

### Iterating on the daemon itself

When changing ic-healthd code, push a locally built binary instead of the
published image:

```bash
just build-healthd
incus-compose healthd down
incus-compose --debug up --healthd-binary ./bin/ic-healthd
```

See [Development: Local Binary](#development-local-binary).

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

## See Also

- [CLI Reference](cli.md#healthd) - healthd management commands
- [Compose Compatibility](compose-compatibility.md) - healthcheck and restart policy support
- [Architecture](architecture.md) - how the sidecar fits the resource model
