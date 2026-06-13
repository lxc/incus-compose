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

## Incus Connection

These environment variables configure how incus-compose connects to an Incus server. For normal use, incus-compose uses your existing Incus CLI configuration via the `--remote` flag or defaults to `local`.

### Variables

| Variable                       | Description                                                                    |
| ------------------------------ | ------------------------------------------------------------------------------ |
| `INCUS_REMOTE`                 | Incus remote name from CLI config (e.g., `local`, `myserver`)                  |
| `INCUS_COMPOSE_IMAGE_CACHE`    | Incus project for image cache (default: `default`)                             |
| `INCUS_COMPOSE_HEALTHD_IMAGE`  | Healthd OCI image to use; {version} is replaced with the incus-compose version |
| `INCUS_COMPOSE_HEALTHD_BINARY` | Path to local ic-healthd binary (uses images:alpine/edge instead of OCI image) |

### Examples

**Using Incus CLI remotes (recommended):**

```bash
# Use a configured remote
incus-compose --remote myserver up

# Or via environment variable
export INCUS_REMOTE=myserver
incus-compose up
```

## See Also

- [CLI Reference](cli.md) - command options and flags
- [Compose Compatibility](compose-compatibility.md) - interpolation and env_file support
