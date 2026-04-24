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
| `--debug`              | Debug logging                                          |

Supports [no-color.org](https://no-color.org/) via `NO_COLOR` env var.

## up

Create and start containers.

```
incus-compose up [SERVICE...]
```

| Option             | Description                                                             |
| ------------------ | ----------------------------------------------------------------------- |
| `--recreate`       | Recreate containers even if they exist                                  |
| `--no-start`       | Don't start containers after creating                                   |
| `--timeout`        | Stop/start timeout seconds (default: 10)                                |
| `--scale`          | Scale service: `web=3` (repeatable)                                     |
| `--no-healthd`     | Don't create healthd sidecar for healthchecks                           |
| `--healthd-binary` | Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image) |

## down

Stop and remove containers.

```
incus-compose down [SERVICE...]
```

| Option         | Description                        |
| -------------- | ---------------------------------- |
| `--project`    | Remove the project                 |
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

## list

List project resources.

```
incus-compose list [SERVICE...]
```

| Option     | Description                 |
| ---------- | --------------------------- |
| `--format` | table (default), yaml, json |
