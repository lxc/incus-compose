# Contributing to incus-compose

Thank you for your interest in contributing! This document outlines the conventions and practices we follow.

This project is destined for the **lxc** org. The org-wide contributing
policy ([lxc/incus CONTRIBUTING](https://github.com/lxc/incus/blob/main/CONTRIBUTING.md)) applies in full,
including:

- **License**: Apache 2.0, no copyright assignment.
- **DCO**: Every commit must carry a `Signed-off-by` line (`git commit -s`).
- **AI tooling**: See the org policy. Contributors must fully own their
  work. AI tools cannot be credited. See also [AGENTS.md](AGENTS.md).

## Philosophy

**KISS** - Keep It Simple, Stupid. As well as "boring" code. These are the guiding principles for all work.

- Prefer shallow package structure over deep nesting
- Direct code over abstractions
- Working software over perfect architecture
- Simple solutions over clever ones
- No non-ASCII characters in code and docs

## Working with Go code

Follow the [Go proverbs](https://go-proverbs.github.io/).

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

- All exported functions and types need doc comments ending with a period
- No misleading comments - if code is self-explanatory, don't comment

### Use of `any`

Avoid using `any` (`interface{}`).
Prefer a small, explicit interface. Use generics only if they clearly reduce duplication.

### Unused parameters

Use `_` for unused parameters rather than ignoring in the function body:

```go
// Preferred
func (t *logTerminal) Read(_ []byte) (int, error) {

// Avoid
func (t *logTerminal) Read(p []byte) (int, error) {
    _ = p
```

### CLI environment variables

- Use `INCUS_COMPOSE_*` prefix for configuration env vars
- Support common standards like [no-color.org](https://no-color.org/) where applicable

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

## Questions?

- Open an issue for bugs or feature requests
- Check existing documentation in `docs/`
