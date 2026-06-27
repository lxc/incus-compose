# CLI Reference

## Global Options

| Option                 | Description                                                                                                                                                 |
| ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-f`, `--file`         | Compose files (repeatable)                                                                                                                                  |
| `-p`, `--project-name` | Project name                                                                                                                                                |
| `--project-directory`  | Working directory                                                                                                                                           |
| `--profile`            | Compose profiles (repeatable)                                                                                                                               |
| `--env-file`           | Environment files (repeatable)                                                                                                                              |
| `-E`, `--os-env`       | Include OS env vars                                                                                                                                         |
| `--remote`             | Incus remote (`INCUS_REMOTE`)                                                                                                                               |
| `--ansi`               | Color output: never/always/auto (`INCUS_COMPOSE_ANSI`)                                                                                                      |
| `--image-cache`        | Incus project used as image cache (`INCUS_COMPOSE_IMAGE_CACHE`, default: `default`), see [Environment Variables](environment-variables.md#incus-connection) |
| `--storage-pool`       | Default storage pool for volumes (`INCUS_COMPOSE_STORAGE_POOL`, default: `detect` for auto-detection)                                                       |
| `--workers`            | Number of concurrent workers (`INCUS_COMPOSE_WORKERS`, default: `10`)                                                                                       |
| `--debug`              | Debug logging                                                                                                                                               |

Supports [no-color.org](https://no-color.org/) via `NO_COLOR` env var.

## up

Create and start containers.

```
incus-compose up [SERVICE...]
```

| Option              | Description                                                                                                                       |
| ------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `-d`, `--detach`    | Detached mode: run containers in the background                                                                                   |
| `--recreate`        | Recreate containers even if they exist                                                                                            |
| `--no-start`        | Don't start containers after creating                                                                                             |
| `--pull`            | Pull policy: `always` (refresh from registry), `missing`/`policy` (use cache if present), `never` (never pull); default: `policy` |
| `--build`           | Rebuild build-configured service images before starting containers                                                                |
| `--no-build`        | Do not build images; fail if a required built image is missing                                                                    |
| `--no-deps`         | Don't start linked services (depends_on)                                                                                          |
| `--timeout`         | Stop/start timeout (default: 1m)                                                                                                  |
| `--dependency-timeout` | Max time to wait for `service_healthy` depends_on (default: 5m; `0` = no limit)                                                |
| `--scale`           | Scale service: `web=3` (repeatable)                                                                                               |
| `--no-healthd`      | Don't create healthd sidecar for healthchecks                                                                                     |
| `--healthd-image`   | Healthd OCI image (`INCUS_COMPOSE_HEALTHD_IMAGE`); `{version}` is replaced with the incus-compose version                          |
| `--healthd-binary`  | Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)                                                    |
| `--healthd-incus`   | Incus API URL healthd connects to (`INCUS_COMPOSE_HEALTHD_INCUS`); overrides `x-incus-compose.healthd.incus`; bridge IP if unset  |
| `--healthd-network` | Network for healthd (`INCUS_COMPOSE_HEALTHD_NETWORK`); overrides `x-incus-compose.healthd.network`; project default if unset      |

Without `--detach`, `up` streams logs from all started services (equivalent to running `logs --follow` immediately after). Use `--detach` to return as soon as containers are started.

For services with `build:`, `up` builds missing images by default. Use `--build` to force a rebuild or `--no-build` to require the image to already exist. See [Builds](build.md) for details.

## build

Build or rebuild service images for services that define `build:`.

```
incus-compose build [SERVICE...]
```

| Option       | Description                                           |
| ------------ | ----------------------------------------------------- |
| `--no-cache` | Do not use cache when building the image              |
| `--pull`     | Always attempt to pull a newer version of base images |

When service names are provided, only matching build-configured services are built. Services without `build:` are skipped. Built images are imported into the Incus project and used by `up`.

See [Builds](build.md) for supported Compose build options and requirements.

## down

Stop and remove containers. Per-project image copies are removed too; volumes and
the image cache are kept (use `--project` to remove everything, including volumes).

```
incus-compose down [SERVICE...]
```

| Option         | Description                                                              |
| -------------- | ------------------------------------------------------------------------ |
| `--project`    | Remove the project (and volumes)                                         |
| `--volumes`    | Alias for `--project` (docker compose compat)                            |
| `--rmi`        | Remove images used by services: `local` or `all` (docker compose compat) |
| `--images`     | Remove known images from the project (equivalent to `--rmi local`)       |
| `--timeout`    | Stop timeout (default: 10s)                                              |
| `--no-deps`    | Don't stop linked services (depends_on)                                  |
| `--no-healthd` | Don't stop/remove healthd sidecar                                        |

## start

Start stopped services.

```
incus-compose start [SERVICE...]
```

| Option         | Description                                                       |
| -------------- | ----------------------------------------------------------------- |
| `--timeout`    | Start timeout (default: 1m)                                       |
| `--with-deps`  | Also start linked services (depends_on) — incus-compose extension |
| `--no-healthd` | Don't start healthd sidecar                                       |

## stop

Stop running services.

```
incus-compose stop [SERVICE...]
```

| Option         | Description                                                      |
| -------------- | ---------------------------------------------------------------- |
| `--timeout`    | Stop timeout (default: 10s)                                      |
| `--with-deps`  | Also stop linked services (depends_on) — incus-compose extension |
| `--no-healthd` | Don't stop healthd sidecar                                       |

## restart

Restart running services.

```
incus-compose restart [SERVICE...]
```

| Option         | Description                                                         |
| -------------- | ------------------------------------------------------------------- |
| `--timeout`    | Stop/start timeout (default: 1m)                                    |
| `--with-deps`  | Also restart linked services (depends_on) — incus-compose extension |
| `--no-healthd` | Don't stop/start healthd sidecar                                    |

### Linked services

`up` and `down` follow `depends_on` by default: naming a service pulls in the
services it links to (its dependencies on `up`, its dependents on `down`). Use
`--no-deps` to act on exactly the named services. On `up`, `--no-deps` also
skips waiting on `depends_on: { condition: service_healthy }` for the
out-of-scope dependencies, so the named service starts without them.

`start`, `stop`, `restart`, `logs`, and `ps` act on exactly the named services
by default, matching `docker compose start`/`stop`. The `--with-deps` flag is an
incus-compose extension that opts those commands into following `depends_on` the
same way `up`/`down` do.

## logs

View container output.

```
incus-compose logs [SERVICE...]
```

| Option           | Description                                                                |
| ---------------- | -------------------------------------------------------------------------- |
| `-f`, `--follow` | Follow output                                                              |
| `--with-deps`    | Also show logs from linked services (depends_on) — incus-compose extension |

Missing instances are skipped with a warning; logs from available instances are still shown.

## config

Validate and render compose file.

```
incus-compose config [SERVICE...]
```

| Option           | Description                              |
| ---------------- | ---------------------------------------- |
| `--format`       | yaml (default) or json                   |
| `-q`, `--quiet`  | Validate only                            |
| `--services`     | List services                            |
| `--volumes`      | List volumes                             |
| `--networks`     | List networks                            |
| `--profiles`     | List profiles                            |
| `--images`       | List images                              |
| `--environment`  | Print environment used for interpolation |
| `--variables`    | Print model variables and default values |
| `-o`, `--output` | Save to file                             |

## exec

Execute a command in a running instance.

```
incus-compose exec [options] SERVICE COMMAND [ARGS...]
```

| Option            | Description                                                          |
| ----------------- | -------------------------------------------------------------------- |
| `-d`, `--detach`  | Run command in the background                                        |
| `--dry-run`       | Execute command in dry run mode                                      |
| `-e`, `--env`     | Set environment variables `KEY=VALUE` (repeatable)                   |
| `--index`         | Index of the container if service has multiple replicas (default: 0) |
| `-T`, `--no-tty`  | Disable pseudo-TTY allocation                                        |
| `--privileged`    | Give extended privileges to the process                              |
| `-u`, `--user`    | Run the command as this user                                         |
| `-w`, `--workdir` | Path to workdir directory for this command                           |

## ps

List containers (instances).

```
incus-compose ps [SERVICE...]
```

| Option          | Description                                                      |
| --------------- | ---------------------------------------------------------------- |
| `-a`, `--all`   | Show all containers (including stopped ones)                     |
| `-q`, `--quiet` | Only display Incus instance names                                |
| `--services`    | Display compose service names instead of instances               |
| `--format`      | table (default) or json                                          |
| `--with-deps`   | Also list linked services (depends_on) — incus-compose extension |

## incus

Run any `incus` command scoped to the current compose project. All flags and arguments are passed through verbatim; only `INCUS_PROJECT` is injected.

```
incus-compose incus COMMAND [ARGS...]
```

Examples:

```bash
incus-compose incus list                        # list instances in this project
incus-compose incus config show web-1           # show instance config
incus-compose incus config set web-1 limits.memory 512MB
incus-compose incus exec web-1 -- bash
```

Equivalent to `INCUS_PROJECT=<project> incus COMMAND [ARGS...]`.

## healthd

Manage the ic-healthd sidecar. See [Health Checking](healthd.md) for full details.

```
incus-compose healthd <subcommand>
```

| Subcommand        | Description                           |
| ----------------- | ------------------------------------- |
| `logs [--follow]` | Stream the ic-healthd container log   |
| `reload`          | Send SIGHUP to the ic-healthd process |
| `restart`         | Restart the ic-healthd container      |
| `up [--recreate]` | Create or recreate the sidecar        |
| `down`            | Stop and remove the sidecar           |

`healthd up` also accepts `--image`, `--binary`, `--incus`, and `--network`. See
[Health Checking - Network Configuration](healthd.md#network-configuration).

## list

List project resources.

```
incus-compose list [SERVICE...]
```

| Option      | Description                              |
| ----------- | ---------------------------------------- |
| `--format`  | table (default), yaml, json              |
| `--healthd` | Include the ic-healthd sidecar in output |

The `IMAGE` column shows the compose image for each service. When `--healthd` is set, the sidecar image is resolved from the instance's stored metadata.

## version

Print the incus-compose version.

```
incus-compose version
```

## self-update

Update incus-compose to the latest release from GitHub.

```
incus-compose self-update
```

This command is only available when both conditions are met:

1. The binary was built with a release version (not `latest` / development builds)
2. The binary file is writable by the current user

When available, `self-update` checks the [lxc/incus-compose](https://github.com/lxc/incus-compose) GitHub releases for a newer version matching the current OS and architecture. If a newer version is found, the binary is replaced in-place. If you are already on the latest version, no action is taken.

## Docker Compose command parity

Most `docker compose` verbs map directly. Anything without a dedicated command is
reachable through the `incus-compose incus` passthrough, which runs any `incus`
command scoped to the current project.

| `docker compose` | incus-compose                            | Notes                                          |
| ---------------- | ---------------------------------------- | ---------------------------------------------- |
| `up`             | `up`                                     |                                                |
| `down`           | `down`                                   |                                                |
| `start` / `stop` / `restart` | `start` / `stop` / `restart` |                                                |
| `ps`             | `ps`                                     |                                                |
| `logs`           | `logs`                                   |                                                |
| `exec`           | `exec`                                   |                                                |
| `build`          | `build`                                  |                                                |
| `config`         | `config`                                 |                                                |
| `pull`           | `up --pull always`                       | No standalone pull; refreshes from registry.   |
| `images`         | `config --images`                        | Or `incus-compose incus image list`.           |
| `cp`             | `incus-compose incus file push` / `pull` |                                                |
| `top`            | `incus-compose incus top`                |                                                |
| `events`         | `incus-compose incus monitor`            |                                                |
| `kill`           | `stop --timeout 0`                       | Forces an immediate stop.                      |
| `run`            | not implemented                          | Use `up` then `exec`.                          |
| `pause` / `unpause` | not implemented                       | Use the `incus-compose incus` passthrough.     |
| `port`           | not implemented                          | Published ports are shown in `config` / `ps`.  |
