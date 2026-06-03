# Health Checking

incus-compose implements health checks via a sidecar container called `ic-healthd`.
Incus has no native healthcheck support, so ic-healthd fills that role.

## How It Works

When `incus-compose up` finds services with a `healthcheck` directive or
`restart: always|on-failure`, it:

1. Creates a restricted Incus trust token scoped to the project.
2. Starts an `ic-healthd` sidecar container and injects the token as a secret.
3. ic-healthd authenticates once (token consumed), persists the resulting cert.
4. ic-healthd discovers which instances to watch by reading the Incus API.
5. ic-healthd runs the health loop and updates instance config keys with the result.

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

## Security

The restricted token gives ic-healthd project-scoped access only:

- Can exec commands into instances in the project.
- Can manage instance state (start/stop/restart) within the project.
- Cannot access other projects or perform global operations.

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

Default image: `registry.gitlab.com:r3j0/incus-compose/ic-healthd:latest`

The container is named `ic-healthd` within the project and tagged with
`user.healthcheck.daemon=true` so ic-healthd skips itself during discovery.
