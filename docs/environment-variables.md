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

```bash
# .env
DATABASE_URL=postgres://localhost/mydb
API_KEY=your-api-key
USER=${USER}

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

These environment variables configure how incus-compose connects to an Incus server. They are primarily for testing with nested Incus instances. For normal use, incus-compose uses your existing Incus CLI configuration.

These variables are rarely needed and are mainly intended for development,
testing, or nested Incus scenarios.

| Variable             | Description                                                     |
| -------------------- | --------------------------------------------------------------- |
| `INCUS_PROJECT`      | The project in which a nested container has been created at     |
| `INCUS_CONTAINER`    | The name of the nested container                                |
| `INCUS_COMPOSE_URL`  | Direct URL to Incus server (e.g., `https://192.168.1.100:8443`) |
| `INCUS_COMPOSE_CERT` | Path to TLS client certificate                                  |
| `INCUS_COMPOSE_KEY`  | Path to TLS client key                                          |

### Example

```bash
export INCUS_PROJECT="dev"
export INCUS_CONTAINER="ict"
export INCUS_COMPOSE_URL="https://192.168.1.100:8443"
export INCUS_COMPOSE_CERT="./certs/client.crt"
export INCUS_COMPOSE_KEY="./certs/client.key"
incus-compose up
```
