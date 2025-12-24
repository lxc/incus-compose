# Test Suite Documentation

This directory contains the comprehensive test suite for incus-compose, focusing on configuration output validation and snapshot testing.

## Test Structure

The test suite uses snapshot testing to ensure consistent behavior across code changes. Tests are organized around different compose scenarios and configurations.

## Test Types

### 1. Config Snapshot Tests (`TestConfigSnapshots`)

Tests the `config` command output against saved snapshots for various scenarios:

- Different output formats (YAML, JSON)
- Service filtering
- Various compose file structures

### 2. Profile Tests (`TestConfigSnapshotsWithProfiles`)

Tests configuration output with different compose profiles:

- Single profiles (`dev`, `monitoring`)
- Multiple profiles (`dev,monitoring`)
- Profile-specific service configurations

### 3. Environment Tests (`TestConfigSnapshotsWithEnv`)

Tests environment variable substitution with different `.env` files:

- Default `.env` file
- Custom environment files (`production.env`, `staging.env`)

### 4. CI Tests (`TestIncusComposeOnly`)

Lightweight test subset that runs in CI environments, focusing on core functionality.

## Environment Variables

### Snapshot Management

- `UPDATE_SNAPSHOTS=1`: Update snapshot files instead of comparing

### CI Detection

- `CI=1`: Automatically set by most CI systems, enables CI-specific test behavior

## Running Tests

### Using Justfile (Recommended)

```bash
# Run all tests (will skip incus facing tests)
just test-local

# Update all snapshots
just update-snapshots

# Create nested dev env.
just dev-install

# Run tests with nested Incus instance
just test

# Clean up nested test environment
just cleanup
```

## Test Fixtures

Test fixtures are located in `test/fixtures/` and represent different compose scenarios:

- `hello_world/`: Simple single-service setup
- `wordpress/`: Multi-service application with database
- `nginx_proxy/`: Network configuration testing
- `with_profiles/`: Profile-based configurations
- `with_env/`: Environment variable testing
- `dev_environment/`: Complex development setup
- `microservices/`: Multi-service architecture
- `postgres_redis/`: Database services testing

Each fixture contains:

- `compose.yaml`: Main compose configuration
- Environment files (`.env`, `production.env`, etc.) where applicable

## Snapshot Organization

Snapshots are stored in `test/snapshots/` with descriptive names:

```
test/snapshots/
├── TestConfigSnapshots-hello_world_yaml
├── TestConfigSnapshots-wordpress_json
├── TestConfigSnapshotsWithProfiles-dev_environment_debug_profile
└── ...
```

## Adding New Tests

### 1. Add a New Test Case

```go
testCases := []ConfigTestCase{
    {
        Name:    "my_new_test_yaml",
        Fixture: "my_fixture",
        Options: &ConfigOptions{
            Format: "yaml",
        },
    },
}
```

### 2. Create Test Fixture

1. Create `test/fixtures/my_fixture/` directory
2. Add `compose.yaml` with your test configuration
3. Add any necessary environment files

### 3. Run Tests to Generate Snapshots

```bash
just update-snapshots
```

The first run will create new snapshot files and might exit with an error. Subsequent runs will compare against these snapshots.

## CI/CD Integration

For continuous integration environments:

```bash
# Standard CI run
go test ./... -v

# CI-specific lightweight tests
CI=1 go test ./test -v -run TestIncusComposeOnly
```

The `CI` environment variable is automatically set by most CI systems (GitHub Actions, GitLab CI, etc.).

## Troubleshooting

### Snapshot Mismatches

If tests fail due to snapshot mismatches:

1. Review the diff in test output
2. Update snapshots if changes are expected: `just update-snapshots`
3. Fix code if changes are unexpected

### Custom Binary Testing

To test with a development build:

```bash
# Build custom binary
just build
```
