# Documentation

User documentation for incus-compose.

## Getting Started

- **[Getting Started](getting-started.md)** - Install, run your first compose project, and learn common workflows
- **[Quick Start (main README)](../README.md#quick-start)** - Minimal example to get running fast

## Using incus-compose

- **[Compose Compatibility](compose-compatibility.md)** - Supported features, limitations, and workarounds
- **[Environment Variables](environment-variables.md)** - How environment variables work (different from docker-compose)

## Understanding incus-compose

- **[Why Incus?](why_incus.md)** - Benefits of Incus over OCI engines like Docker

## Planning and Development

- **[Roadmap](roadmap.md)** - Planned features and improvements

## Quick Reference

### Common Commands

```bash
# Start services
incus-compose up

# Start without starting containers
incus-compose up --no-start

# Recreate containers
incus-compose up --recreate

# Stop and remove
incus-compose down

# Also remove volumes
incus-compose down --volumes

# List running containers
incus-compose ps

# List all containers (including stopped)
incus-compose ps --all

# Validate compose file
incus-compose config --quiet

# Show resolved configuration
incus-compose config

# Show specific parts
incus-compose config --services
incus-compose config --networks
incus-compose config --volumes
incus-compose config --environment
```

### Common Patterns

**Basic service:**
```yaml
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
```

**With dependencies:**
```yaml
services:
  db:
    image: docker.io/postgres:16-alpine
    
  app:
    image: docker.io/myapp:latest
    depends_on:
      - db
```

**With volumes:**
```yaml
services:
  app:
    image: docker.io/myapp:latest
    volumes:
      - data:/var/lib/app
      - ./config:/etc/app:ro

volumes:
  data:
```

**With environment:**
```yaml
services:
  app:
    image: docker.io/myapp:latest
    environment:
      DATABASE_URL: ${DATABASE_URL}
    env_file:
      - .env
```

## Key Differences from Docker Compose

1. **Environment variables** - OS env not included by default (use `--os-env` or `.env` files)
2. **Bind mounts** - Only work with local Incus (not remote connections)
3. **Networks** - Names may be hashed if longer than 13 chars
4. **Volumes** - Automatic UID/GID shifting based on container image
5. **IP addresses** - Real IPs on your network (not NAT)

## Need Help?

- **Bugs/Features**: Open an issue on GitHub
- **Questions**: Check existing documentation or open a discussion
- **Contributing**: See `.rules` file for development conventions
