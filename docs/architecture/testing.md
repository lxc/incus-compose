# Testing Guide

This guide covers testing patterns, fixtures, and best practices for incus-compose.

## Running Tests

Use `just --list` to see all available commands. Below is the complete reference:

### Test Commands

| Command                                         | Description                                                    |
| ----------------------------------------------- | -------------------------------------------------------------- |
| `just test`                                     | Run all tests against nested Incus (preferred also runs in CI) |
| `just test ./client/...`                        | Run tests for specific package                                 |
| `just test -v -run TestName`                    | Run specific test with verbose output                          |
| `just test-local`                               | Run unit tests only (no Incus connection required)             |
| `just test-slow`                                | Run tests that take long to run                                |
| `just update-snapshots`                         | Update all snapshot test files                                 |
| `just update-snapshots ./cmd/incus-compose/...` | Update snapshots for specific package                          |
| `just update-slow-snapshots`                    | Update snapshot for slow test files                            |

### Development Commands

| Command                 | Description                                  |
| ----------------------- | -------------------------------------------- |
| `just build`            | Build the binary                             |
| `just run <args>`       | Run incus-compose via `go run` (uses `.env`) |
| `just run-local <args>` | Run against local Incus (ignores `.env`)     |
| `just incus <args>`     | Run commands in the nested Incus container   |

### Code Quality

| Command           | Description                              |
| ----------------- | ---------------------------------------- |
| `just lint`       | Lint all files with golangci-lint        |
| `just fix`        | Fix lint issues with golangci-lint       |
| `just pre-commit` | Run before committing (tidy, lint, test) |

### Setup & Maintenance

| Command            | Description                         |
| ------------------ | ----------------------------------- |
| `just dev-install` | Create nested Incus dev environment |

## Test Organization

Tests live alongside the code they test:

```
client/
  ├── client.go
  ├── client_test.go      # Tests for client.go
  ├── resources.go
  └── resources_test.go   # Tests for resources.go
project/
  ├── project.go
  └── project_test.go     # Tests for project.go
```

## Unit Tests

Unit tests use mock resources that implement `client.ResourceOperation` interface. They require no Incus connection and run fast.

**Examples**: `client/operation_test.go`, `client/resources_test.go`

**Run with**:

```bash
just test-local
```

### Mock Pattern

Mocks implement the same interfaces as real resources:

```go
type MockInstance struct {
    name     string
    kind     string
    priority int
    exists   bool
    done     bool
    error    error
}

func (m *MockInstance) Name() string { return m.name }
func (m *MockInstance) Kind() string { return m.kind }
func (m *MockInstance) Priority() int { return m.priority }
func (m *MockInstance) Exists() bool { return m.exists }
func (m *MockInstance) Done() bool { return m.done }
func (m *MockInstance) Error() error { return m.error }
func (m *MockInstance) Handle() error { return m.error }
```

## Integration Tests

Integration tests use real nested Incus instances and test actual API interactions.

**Run with**:

```bash
just test
```

### Image Cache

Tests use a dedicated cache project (`incus-compose-tests-cache`) separate from the CLI's image cache (the `default` project unless `--image-cache` is set). This keeps test images isolated and avoids polluting the user's cache.

The test cache is configured via `ClientProvideConnection` in test setup, pointing to a test-specific project.

### Environment Setup

The nested Incus environment is configured via `.env` file:

- `INCUS_REMOTE` - The remote to use.

Theres also `just test-slow` this includes slow as in long running tests.

**First-time setup**:

```bash
just dev-install
```

## Test Fixtures

Located in `test/fixtures/`. Each fixture is a minimal compose scenario.

### Available Fixtures

- `simple-nginx/` - Simplest case
- `wordpress/` - Multi-service with volumes
- `with_profiles/` - Profile testing
- `with_env/` - Environment variable testing
- `with-secrets/` - Secrets management testing
- `with-restart/` - Restart policies testing

### Fixture Guidelines

**Snapshot portability**: Normalize absolute paths before snapshotting:

```go
output = strings.ReplaceAll(output, fixturePath, "$FIXTURE_PATH")
```

**Self-contained fixtures**: Define env vars like `$USER` or `$HOME` in `.env` to avoid OS dependencies:

```env
USER=testuser
HOME=/home/testuser
```

**Pure YAML**: Compose files should be pure YAML without comments:

```yaml
services:
  web:
    image: images:alpine/edge
    ports:
      - "8080:80"
```

### Snapshot Tests

Snapshots live in `test/snapshots/` and are named by test function and case.

**Update snapshots**:

```bash
just update-snapshots
```

**Snapshot naming**: `TestFunctionName_TestCase.yaml`

### Common Workflows

```bash
# Run a single test verbosely
just test -v -run TestInstanceSecretSuite

# Run tests for a specific package
just test ./client/...

# Quick validation before commit
just pre-commit

# Test a compose file
just run -f test/fixtures/simple-nginx/compose.yaml config
```

## Best Practices

1. **Test isolation** - Each test gets fresh resources via `SetupTest()`
2. **Error aggregation** - Use `errors.Join()` for batch operation errors
3. **Priority testing** - Verify creation/deletion order respects priorities
4. **Mock consistency** - Mocks should behave like real resources
5. **Fixture reuse** - Share fixtures across tests but keep them minimal
6. **Snapshot hygiene** - Review snapshot diffs carefully during updates

## See Also

- [Contributing](../../CONTRIBUTING.md) - coding, style, and workflow rules
- [Architecture](../architecture.md) - the design these tests exercise
- [Client Package](client/README.md) - Stack and resource internals
