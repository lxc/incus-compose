# Test Fixtures

This directory contains real-world compose file examples used for testing incus-compose.

## Fixture Directory Structure

### `hello_world/`
**Purpose:** Basic single-service test
- Simple service with image reference
- Minimal configuration
- Used for basic loading tests

### `wordpress/`
**Purpose:** WordPress + MySQL stack
- Multiple services with dependencies
- Named volumes
- Environment variables
- Service dependencies (`depends_on`)
- Common CMS deployment pattern

### `postgres_redis/`
**Purpose:** API application with PostgreSQL and Redis
- Three-tier application (API, database, cache)
- Health checks
- Environment variable substitution with defaults
- Includes `.env` file
- Common web application pattern

### `nginx_proxy/`
**Purpose:** Nginx reverse proxy with multiple backends
- Multiple networks (frontend/backend)
- Internal networks
- Network isolation
- Reverse proxy pattern

### `microservices/`
**Purpose:** Complex microservices architecture
- Multiple services (8+)
- Different database types (PostgreSQL, MySQL, MongoDB)
- Message queue (Kafka)
- Shared cache (Redis)
- Service isolation with multiple networks
- Environment variable usage
- Includes `.env` file
- Enterprise-grade architecture

### `dev_environment/`
**Purpose:** Development environment with multiple profiles
- Core services (always running)
- Profile-based services:
  - `debug`: Development tools (pgadmin, mailhog)
  - `cache`: Redis for caching
  - `monitoring`: Prometheus + Grafana
- Multiple profile combinations
- Real development workflow

### `with_profiles/`
**Purpose:** Profile functionality testing
- Services with different profile assignments
- Multiple profiles per service
- Profile combinations:
  - `dev`: Development tools
  - `test`: Testing infrastructure
  - `prod`: Production services
  - `monitoring`: Monitoring stack
  - `debug`: Debugging tools

### `with_env/`
**Purpose:** Environment variable loading
- Default `.env` file
- Custom environment files (`production.env`, `staging.env`)
- Environment variable substitution
- Default value handling
- OS environment variable usage

### `multiple_files/`
**Purpose:** Multiple compose file testing
- Base `compose.yaml`
- Override `compose.override.yaml` (automatically loaded)
- Test-specific `compose.test.yaml` (manual load)
- Service overrides and merging
- Common CI/CD pattern

### `invalid/`
**Purpose:** Error handling testing
- Invalid compose syntax
- Missing required fields
- Used to test error cases

## Running Tests

From the project root:

```bash
# Run all tests
go test ./test/...

# Run with verbose output
go test -v ./test/...

# Run specific test
go test -v ./test -run TestLoadProjectSuite/TestLoadBasicProject

# Run with coverage
go test -cover ./test/...
```

## Adding New Fixtures

When adding new fixtures:

1. Create a descriptive directory name
2. Add a real-world use case (not contrived examples)
3. Include relevant files (`.env`, multiple compose files, etc.)
4. Add corresponding test cases in `loadproject_test.go`
5. Update this README with the fixture description

## Fixture Design Principles

- **Real-world examples**: Use actual deployment patterns
- **Representative**: Cover common use cases
- **Complete**: Include all necessary files (env files, etc.)
- **Documented**: Clear purpose and what it tests
- **Maintainable**: Keep fixtures simple but realistic
