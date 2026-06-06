# CLI Reference

## Global Options

| Option                 | Description                                            |
| ---------------------- | ------------------------------------------------------ |
| `-f`, `--file`         | Compose files (repeatable)                             |
| `-p`, `--project-name` | Project name                                           |
| `--project-directory`  | Working directory                                      |
| `--profile`            | Compose profiles (repeatable)                          |
| `--env-file`           | Environment files (repeatable)                         |
| `-E`, `--os-env`       | Include OS env vars                                    |
| `--remote`             | Incus remote (`INCUS_REMOTE`)                          |
| `--ansi`               | Color output: never/always/auto (`INCUS_COMPOSE_ANSI`) |
| `INCUS_COMPOSE_IMAGE_CACHE` | Incus project for image cache (default: `default`) |
| `--debug`              | Debug logging                                          |

Supports [no-color.org](https://no-color.org/) via `NO_COLOR` env var.

## up

Create and start containers.

```
incus-compose up [SERVICE...]
```

| Option             | Description                                                             |
| ------------------ | ----------------------------------------------------------------------- |
| `-d`, `--detach`   | Detached mode: run containers in the background                         |
| `--recreate`       | Recreate containers even if they exist                                  |
| `--no-start`       | Don't start containers after creating                                   |
| `--pull`           | Pull policy: `always` (refresh from registry), `missing`/`policy` (use cache if present), `never` (never pull); default: `policy` |
| `--timeout`        | Stop/start timeout seconds (default: 10)                                |
| `--scale`          | Scale service: `web=3` (repeatable)                                     |
| `--no-healthd`      | Don't create healthd sidecar for healthchecks                           |
| `--healthd-binary`  | Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image) |
| `--healthd-network` | Incus bridge for healthd (`INCUS_COMPOSE_HEALTHD_NETWORK`); overrides `x-incus-compose.healthd-network`; auto-detects if unset |

Without `--detach`, `up` streams logs from all started services (equivalent to running `logs --follow` immediately after). Use `--detach` to return as soon as containers are started.

## down

Stop and remove containers. Per-project image copies are removed too; volumes and
the image cache are kept (use `--project` to remove everything, including volumes).

```
incus-compose down [SERVICE...]
```

| Option         | Description                        |
| -------------- | ---------------------------------- |
| `--project`    | Remove the project (and volumes)   |
| `--timeout`    | Stop timeout seconds (default: 10) |
| `--no-healthd` | Don't stop/remove healthd sidecar  |

## start

Start stopped services.

```
incus-compose start [SERVICE...]
```

| Option         | Description                         |
| -------------- | ----------------------------------- |
| `--timeout`    | Start timeout seconds (default: 10) |
| `--no-healthd` | Don't start healthd sidecar         |

## stop

Stop running services.

```
incus-compose stop [SERVICE...]
```

| Option         | Description                        |
| -------------- | ---------------------------------- |
| `--timeout`    | Stop timeout seconds (default: 10) |
| `--no-healthd` | Don't stop healthd sidecar         |

## restart

Restart running services.

```
incus-compose restart [SERVICE...]
```

| Option         | Description                              |
| -------------- | ---------------------------------------- |
| `--timeout`    | Stop/start timeout seconds (default: 10) |
| `--no-healthd` | Don't stop/start healthd sidecar         |

## logs

View container output.

```
incus-compose logs [SERVICE...]
```

| Option           | Description   |
| ---------------- | ------------- |
| `-f`, `--follow` | Follow output |

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

| Option          | Description                                          |
| --------------- | ---------------------------------------------------- |
| `-a`, `--all`   | Show all containers (including stopped ones)         |
| `-q`, `--quiet` | Only display Incus instance names                    |
| `--services`    | Display compose service names instead of instances   |
| `--format`      | table (default) or json                              |

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

| Subcommand          | Description                                          |
| ------------------- | ---------------------------------------------------- |
| `logs [--follow]`   | Stream the ic-healthd container log                  |
| `reload`            | Send SIGHUP to the ic-healthd process                |
| `restart`           | Restart the ic-healthd container                     |
| `up [--recreate]`   | Create or recreate the sidecar                       |
| `down`              | Stop and remove the sidecar                          |

`healthd up` also accepts `--image`, `--binary`, and `--network`.

## list

List project resources.

```
incus-compose list [SERVICE...]
```

| Option     | Description                 |
| ---------- | --------------------------- |
| `--format` | table (default), yaml, json |
