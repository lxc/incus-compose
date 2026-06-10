# Testing Guide

This guide covers testing patterns, fixtures, and best practices for incus-compose.

## Overview

We use `testify/suite` for all tests. Tests are organized into two categories:

- **Unit tests** - Fast, isolated tests using mocks
- **Integration tests** - Tests against real nested Incus instances

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

### Principles

**Do**:

- Use `testify/suite` pattern for each `_test.go` file
- Copy-paste setup code between test files (KISS over DRY)
- Keep each test suite self-contained

**Don't create**:

- Shared test base suites or abstractions
- Test helper packages

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

Tests use a dedicated cache project separate from the CLI's `incus-compose-images` project. This keeps test images isolated and avoids polluting the user's cache.

The test cache is configured via `ClientProvideConnection` in test setup, pointing to a test-specific project.

### Environment Setup

The nested Incus environment is configured via `.env` file:

- `INCUS_REMOTE` - The remote to use.

Theres also `just test-slow` this includes slow as in long running tests.

**First-time setup**:

```bash
just dev-install
```

## Compose Stack Test Pattern

For tests needing multiple resources (profiles, images, networks, volumes, instances), use the `ComposeStack` pattern.

### ComposeStack Structure

```go
type ComposeStack struct {
    Profiles       []client.ResourceOperation
    Images         []client.ResourceOperation
    Networks       []client.ResourceOperation
    StorageVolumes []client.ResourceOperation
    Instances      []client.ResourceOperation
}
```

### Using ComposeStack in Tests

**In SetupTest** - create fresh stack per test:

```go
func (s *TestSuite) SetupTest() {
    s.stack, err = createComposeStack(nil)  // nil = mocks
    s.Require().NoError(err)
}
```

**In test cases**:

```go
// All resources in priority order
resources: s.stack.AllResources()

// Just instances
resources: s.stack.Instances

// Specific resource types
resources: append(s.stack.Networks, s.stack.Instances...)
```

### Mock vs Real Stacks

**Mock stack** (for unit tests):

```go
s.stack, err = createComposeStack(nil)  // nil parameter = mocks
```

**Real stack** (for integration tests):

```go
s.stack, err = createComposeStack(project)  // real project = actual resources
```

The same test code works for both because both implement `client.ResourceOperation`.

### When to Use ComposeStack

**Use ComposeStack when**:

- Testing resource creation order
- Testing rollback behavior across multiple resources
- Simulating full compose-like scenarios

**Use inline mocks when**:

- Testing single operations
- Focused error handling tests
- Simple validation logic

```go
resources: []client.ResourceOperation{
    NewMockInstance("web", errors.New("handle failed")).Ensure(true),
}
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

## Running Tests

Use `just --list` to see all available commands. Below is the complete reference:

### Test Commands

| Command                                         | Description                                        |
| ----------------------------------------------- | -------------------------------------------------- |
| `just test`                                     | Run all tests against nested Incus                 |
| `just test ./client/...`                        | Run tests for specific package                     |
| `just test -v -run TestName`                    | Run specific test with verbose output              |
| `just test-local`                               | Run unit tests only (no Incus connection required) |
| `just test-coverage`                            | Run tests with coverage report                     |
| `just update-snapshots`                         | Update all snapshot test files                     |
| `just update-snapshots ./cmd/incus-compose/...` | Update snapshots for specific package              |

### Development Commands

| Command                 | Description                                  |
| ----------------------- | -------------------------------------------- |
| `just build`            | Build the binary                             |
| `just run <args>`       | Run incus-compose via `go run` (uses `.env`) |
| `just run-debug <args>` | Run with debug output enabled                |
| `just run-local <args>` | Run against local Incus (ignores `.env`)     |
| `just incus <args>`     | Run commands in the nested Incus container   |

### Code Quality

| Command           | Description                                    |
| ----------------- | ---------------------------------------------- |
| `just lint`       | Lint all files with golangci-lint              |
| `just pre-commit` | Run before committing (tidy, lint, test-local) |

### Setup & Maintenance

| Command            | Description                         |
| ------------------ | ----------------------------------- |
| `just dev-install` | Create nested Incus dev environment |
| `just clean`       | Remove generated data               |

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

## Common Patterns

### Table-Driven Tests

```go
type operationTest struct {
    name      string
    operation *client.Operation
    wantDone  bool
    wantErr   bool
    validate  func(*testing.T, *client.Operation)
}

func (s *TestSuite) TestOperation() {
    tests := []operationTest{
        {
            name: "successful operation",
            operation: client.NewDoneOperation(context.Background()),
            wantDone: true,
            wantErr: false,
        },
    }

    for _, tt := range tests {
        s.Run(tt.name, func() {
            if tt.validate != nil {
                tt.validate(s.T(), tt.operation)
            }
        })
    }
}
```

### Error Testing

```go
err := operation.Handle()
s.Error(err)
s.ErrorIs(err, client.ErrDisconnected)
```

### Context Testing

```go
ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
defer cancel()

op := client.NewOperation(ctx, handler)
err := op.Handle()
s.ErrorIs(err, context.DeadlineExceeded)
```
