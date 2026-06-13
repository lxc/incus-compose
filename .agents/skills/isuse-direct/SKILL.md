---
name: isuse-direct
description: Execute a specified incus-compose issue directly without plan mode, after loading project context.
---

# Execute an Issue Directly

Use this skill when the user invokes `/isuse-direct` or explicitly asks to execute a specified issue without plan mode.

The skill name intentionally matches the existing Claude command filename `isuse-direct.md`.

## Steps

1. Load the `hello` skill context first, unless it has already been loaded in this session.
2. If the user did not provide an issue number, title, or description, ask for it before continuing.
3. Execute the issue directly without entering a separate planning-only phase.
4. Still inspect relevant existing patterns before editing code.
5. Make focused changes only for the requested issue.
6. Validate with appropriate `just` commands from `docs/architecture/testing.md`.

## Constraints

- Use `just` commands instead of raw `go` commands.
- Follow `CONTRIBUTING.md`, `docs/architecture.md`, and `docs/architecture/testing.md`.
- Do not commit changes unless the user explicitly requests it.
