# Environment Variables

incus-compose handles environment variables differently than docker-compose for security and reproducibility reasons.

## How It Works

### Default Behavior

By default, incus-compose loads environment variables from:

1. `.env` file in the compose file's directory
2. Files specified with `--env-file`

These `.env` files **can reference OS environment variables** for interpolation:

```env
# .env
DB_PASSWORD=secret123
HOME_DIR=${HOME}
CURRENT_USER=${USER}
```

Only variables explicitly defined in `.env` files are passed to your compose project. Your shell's environment (like `PATH`, `EDITOR`, etc.) is **not** automatically included.

### Why This Matters

- **Security**: Sensitive environment variables from your shell don't accidentally leak into containers
- **Reproducibility**: The same compose file behaves the same way on different machines
- **Explicitness**: You always know exactly which variables are available

## The `--os-env` / `-E` Flag

If you need full docker-compose compatibility, use the `--os-env` flag:

```bash
incus-compose --os-env up
incus-compose -E up
```

This includes all OS environment variables directly, matching docker-compose behavior.

## Examples

### Using .env files (recommended)

```env
# .env
DATABASE_URL=postgres://localhost/mydb
API_KEY=your-api-key
USER=${USER}
```

```yaml
# compose.yaml
services:
  app:
    environment:
      DATABASE_URL: ${DATABASE_URL}
      API_KEY: ${API_KEY}
      DEPLOYED_BY: ${USER}
```

```bash
incus-compose up
```

### Using --os-env for compatibility

```bash
export DATABASE_URL=postgres://localhost/mydb
incus-compose --os-env up
```

## Quick Reference

| Method     | Variables Available                         | Use Case                                    |
| ---------- | ------------------------------------------- | ------------------------------------------- |
| Default    | `.env` files only (can interpolate OS vars) | Production, CI/CD                           |
| `--os-env` | All OS environment variables                | Quick testing, docker-compose compatibility |

## CLI Configuration

Every global flag can be set via an environment variable. Flags given on the command line take precedence over environment variables.

### Project and Files

| Variable                          | Flag                        | Description                                                  |
| --------------------------------- | --------------------------- | ------------------------------------------------------------ |
| `INCUS_COMPOSE_FILE`              | `--file`, `-f`              | Compose configuration files (comma-separated for multiple)   |
| `INCUS_COMPOSE_PROJECT_NAME`      | `--project-name`, `-p`      | Project name                                                 |
| `INCUS_COMPOSE_PROJECT_DIRECTORY` | `--project-directory`, `-P` | Working directory                                            |
| `INCUS_COMPOSE_ENV_FILE`          | `--env-file`                | Alternative environment files (comma-separated for multiple) |
| `INCUS_COMPOSE_PROFILES`          | `--profile`                 | Profiles to enable (comma-separated for multiple)            |

### Incus Connection

| Variable                     | Flag             | Description                                                   |
| ---------------------------- | ---------------- | ------------------------------------------------------------- |
| `INCUS_REMOTE`               | `--remote`       | Incus remote name from CLI config (e.g., `local`, `myserver`) |
| `INCUS_COMPOSE_IMAGE_CACHE`  | `--image-cache`  | Incus project for image cache (default: `default`)            |
| `INCUS_COMPOSE_STORAGE_POOL` | `--storage-pool` | Default storage pool (default: `detect`)                      |

### Display and Debugging

| Variable                | Flag        | Description                                                      |
| ----------------------- | ----------- | ---------------------------------------------------------------- |
| `INCUS_COMPOSE_ANSI`    | `--ansi`    | Control ANSI output: `never`, `always`, `auto` (default: `auto`) |
| `INCUS_COMPOSE_DEBUG`   | `--debug`   | Enable debug logging (`true`/`1`)                                |
| `INCUS_COMPOSE_WORKERS` | `--workers` | Number of concurrent workers (default: `10`)                     |
| `NO_COLOR`              | --          | Disable color output ([no-color.org](https://no-color.org/))     |

### Healthd

| Variable                        | Flag                | Description                                                                      |
| ------------------------------- | ------------------- | -------------------------------------------------------------------------------- |
| `INCUS_COMPOSE_HEALTHD_IMAGE`   | `--healthd-image`   | Healthd OCI image; `{version}` is replaced with the incus-compose version        |
| `INCUS_COMPOSE_HEALTHD_BINARY`  | `--healthd-binary`  | Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image)   |
| `INCUS_COMPOSE_HEALTHD_INCUS`   | `--healthd-incus`   | Incus API URL healthd connects to (default: bridge IP + client port)             |
| `INCUS_COMPOSE_HEALTHD_NETWORK` | `--healthd-network` | Network healthd attaches to: `<project>:<network>`, a bridge, or empty (default) |

### Examples

```bash
# Use a configured Incus remote
export INCUS_REMOTE=myserver
incus-compose up

# Set project defaults in your shell profile
export INCUS_COMPOSE_FILE=compose.yaml,compose.prod.yaml
export INCUS_COMPOSE_PROJECT_NAME=myapp
incus-compose up

# Debug with extra workers
INCUS_COMPOSE_DEBUG=1 INCUS_COMPOSE_WORKERS=20 incus-compose up
```

## See Also

- [CLI Reference](cli.md) - command options and flags
- [Compose Compatibility](compose-compatibility.md) - interpolation and env_file support
