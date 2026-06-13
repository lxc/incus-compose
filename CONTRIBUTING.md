# Contributing to incus-compose

Thank you for your interest in contributing! This document outlines the conventions and practices we follow.

## Philosophy

**KISS** - Keep It Simple, Stupid. As well as "boring" code. These are the guiding principles for all work.

- Prefer shallow package structure over deep nesting
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
|-- cmd/ic-healthd/       # Sidecar
├── client/               # High-level Incus client wrapper
|-- examples/             # Example projects
├── project/              # Compose project loading and service translation
├── docs/                 # User-facing documentation
└── test/                 # Tests and fixtures
```

**Package Guidelines**:

- `cmd/incus-compose/` - CLI flag parsing, command handlers, wiring only
- `cmd/ic-healthd/` - Sidecar for health checking and instance restarts
- `client/` - High-level Incus wrapper, resource management, transactions
- `examples/` - Example projects ready to use with incus-compose
- `project/` - Compose-spec loading via compose-go, service-to-instance translation
- Root package - No code at root level (all in packages)

**Don't create**:

- Deep nesting like `pkg/application/container/`
- Abstraction layers "for future flexibility"

## Build and Test Commands

See [docs/architecture/testing.md](docs/architecture/testing.md) for the complete command reference and testing patterns.

Quick start:

```bash
just --list       # Show all available commands
just pre-commit   # Run before committing
just dev-install  # First-time setup for nested Incus
```

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

For comprehensive testing documentation including patterns, fixtures, and best practices, see [docs/architecture/testing.md](docs/architecture/testing.md).

Tests are categorized as unit tests (using mocks) or integration tests (using real Incus instances).

## Docker Compose Compatibility

Output should match `docker compose config` where possible.

**Intentional differences**:

- OS env vars not included by default (use `--os-env` for compatibility)

## Style Preferences

These aren't hard rules, but following them helps maintain consistency:

### Keep it direct

Avoid intermediate variables when the expression is clear:

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

## Exploring Incus and Docker Compose

When implementing features that interact with Incus or Docker Compose:

1. Use `go doc` to check API types (e.g., `go doc github.com/lxc/incus/v7/client.InstanceServer`)
2. Look at vendor source for implementation patterns
3. Test with real tools (`docker-compose`, `incus`) to verify expected behavior
4. Compare output for compatibility

## Questions?

- Open an issue for bugs or feature requests
- Check existing documentation in `docs/`
