# Contributing to incus-compose

Thank you for your interest in contributing! This document outlines the conventions and practices we follow.

## Philosophy

**KISS** - Keep It Simple, Stupid. As well as "boring" code. These are the guiding principles for all work.

- Prefer shallow package structure over deep nesting
- Use `internal/` for implementation details
- Direct code over abstractions
- Working software over perfect architecture
- Simple solutions over clever ones
- No non-ASCII characters in code and docs

## Working with Go code

Make sure to follow these proverbs, they are partially copied from [go-proverbs](https://go-proverbs.github.io/).

- Don't communicate by sharing memory, share memory by communicating.
- Concurrency is not parallelism.
- Channels orchestrate; mutexes serialize.
- The bigger the interface, the weaker the abstraction.
- Make the zero value useful.
- interface{} says nothing.
- Gofmt's style is no one's favorite, yet gofmt is everyone's favorite.
- A little copying is better than a little dependency.
- With the unsafe package there are no guarantees.
- Clear is better than clever.
- Reflection is never clear.
- Errors are values.
- Don't just check errors, handle them gracefully.
- Design the architecture, name the components, document the details.
- Documentation is for users.
- Don't panic.

## Architecture and design rules

Its core design principles are documented in [docs/architecture.md](docs/architecture.md).

Before contributing, you **must** read and understand this document.
It defines non-negotiable boundaries, including:

- What incus-compose will and will not implement
- Where Compose semantics must remain untouched
- How mapping to Incus is structured
- Which layers are allowed to change behavior

Contributions that violate these principles will be rejected, regardless of
feature completeness or test coverage.

## Project Structure

```
incus-compose/
├── cmd/incus-compose/    # CLI entry point
├── client/               # High-level Incus client wrapper
├── project/              # Compose project loading and service translation
├── internal/             # Private implementation details (use as needed)
├── docs/                 # User-facing documentation
└── test/                 # Tests and fixtures
```

**Package Guidelines**:

- `cmd/incus-compose/` - CLI flag parsing, command handlers, wiring only
- `client/` - High-level Incus wrapper, resource management, transactions
- `project/` - Compose-spec loading via compose-go, service-to-instance translation
- `internal/` - Implementation details that shouldn't be imported externally
- Root package - No code at root level (all in packages)

**Don't create**:

- Deep nesting like `pkg/application/container/`
- Abstraction layers "for future flexibility"

## Build and Test Commands

```bash
just build              # Build the binary
just lint               # Run linters
just test               # Run all tests against nested Incus
just test-local         # Run local unit tests (no Incus needed)
just update-snapshots   # Update test snapshots
```

**Development workflow**:

1. `just dev-install` - Set up nested Incus (first time only)
2. `just run -f test/fixtures/simple-nginx/compose.yaml config` - Test a command
3. `just test` - Run full test suite
4. `just clean` - Clean up when done

## Code Style

### Naming

Prefer Go-style concise names over Java-style verbose names:

| Prefer     | Avoid                 |
| ---------- | --------------------- |
| `Copied()` | `IsCopiedToProject()` |
| `Status()` | `GetCurrentStatus()`  |
| `Valid()`  | `IsValidInstance()`   |
| `err`      | `errorResult`         |

Go code reads better when names are short and context provides meaning.

### Comments

- All exported functions and types need doc comments
- No misleading comments - if code is self-explanatory, don't comment

### Use of `any`

Avoid using `any` (`interface{}`).
Prefer a small, explicit interface. Use generics only if they clearly reduce duplication.

### Error Handling

**Return errors, don't panic**:

```go
if err != nil {
    return fmt.Errorf("creating container %s: %w", name, err)
}
```

**Aggregate errors for batch operations**:

```go
var errs error
for _, item := range items {
    if err := operation(item); err != nil {
        errs = errors.Join(errs, err)
    }
}
return errs
```

**Use sentinel errors**:

```go
var (
    ErrDisconnected       = errors.New("trying to use a disconnected client")
    ErrInstanceNotRunning = errors.New("the instance is not running")
)
```

**Check errors with errors.Is(), not string contains**:

```go
// Bad
if strings.Contains(err.Error(), "not found") { }

// Good
if errors.Is(err, ErrNotFound) { }
```

### Imports

- Use gci for import ordering (enforced by linter)
- Group: stdlib, external, internal

### Commit Messages

Use conventional commit format with package scope:

```
<type>(<package>): <description>
```

**Types**:

- `fix` - Bug fix
- `feat` - New feature or improvement
- `chore` - API or CLI API change

**Examples**:

```
fix(client): handle nil pointer in image cache
feat(cmd): add --timeout flag to up command
chore(client): rename Resource interface method
```

## Testing

For comprehensive testing documentation including patterns, fixtures, and best practices, see [docs/testing.md](docs/testing.md).

We use `testify/suite` for all tests. Tests are categorized as unit tests (using mocks) or integration tests (using real Incus instances).

## Docker Compose Compatibility

Output should match `docker compose config` where possible.

**Intentional differences**:

- OS env vars not included by default (use `--os-env` for compatibility)

## Style Preferences

These aren't hard rules, but following them helps maintain consistency:

### Keep it direct

Avoid intermediate variables when the expression is clear:

```go
// Preferred
err = r.client.hookOperation(r.client.globalClient.Ctx, ActionStop, r, options, op, err)

// Avoid
ctx := r.client.globalClient.Ctx
err = r.client.hookOperation(ctx, ActionStop, r, options, op, err)
```

### Unused parameters

Use `_` for unused parameters rather than ignoring in the function body:

```go
// Preferred
func (t *logTerminal) Read(_ []byte) (int, error) {

// Avoid
func (t *logTerminal) Read(p []byte) (int, error) {
    _ = p
```

### Reuse existing patterns

Before adding new functionality, check if similar patterns exist in the codebase. For example, if there's already a helper function or flag handling logic, extend it rather than creating something new.

### CLI environment variables

- Use `INCUS_COMPOSE_*` prefix for configuration env vars
- Support common standards like [no-color.org](https://no-color.org/) where applicable

## Guidelines

### Don't

- Don't create abstractions before they're needed
- Don't add features not in the compose spec
- Don't shell out when the Go API exists
- Don't duplicate compose-go types with custom structs
- Don't over-engineer for hypothetical future needs

### Do

- Keep functions small and focused
- Test with real compose files
- Compare with docker-compose behavior
- Ask "is this simpler?" before adding code
- Delete code that isn't pulling its weight

## Documentation

- Keep user docs minimal and practical
- All exported functions/types must have doc comments ending with a period

## Questions?

- Open an issue for bugs or feature requests
- Check existing documentation in `docs/`
