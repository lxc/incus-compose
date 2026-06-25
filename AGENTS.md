# AGENTS.md

AI-specific and meta rules for working in this repository.

This project is destined for the **lxc** org, so the org-wide agent rules below
(adapted from `lxc/incus` `AGENTS.md`) apply and take precedence.

To get an idea about the project read [README.md](README.md).

## Rule hierarchy

1. The org rules in this file (Legal, Formatting) - non-negotiable.
2. [CONTRIBUTING.md](CONTRIBUTING.md) - canonical coding, architecture, testing,
   and workflow rules; recursively read the docs it references.
3. `AGENTS.local.md` - personal collaboration notes (untracked, local only).

Resolve conflicts upward: org rules beat CONTRIBUTING.md, which beats local notes.
Do not restate or reinterpret project rules locally. Everything not fixed here is
discussable - always ask before guessing.

## Legal

- All contributions to this repository must be compatible with the Apache 2.0 license.
- Specifically (but not limited to), contributions cannot include code licensed under the terms of the GPL, AGPL or LGPL licenses.
- Only human beings are allowed to sign the Developer Certificate of Ownership (DCO / Signed-off-by).
- Only human beings can ever be credited within commit messages.

## Formatting

- Code comments should be no longer than one line, unless they are required to cover complex unintuitive logic.
- Commit messages should similarly be kept as short and to the point as possible, no need to summarize the whole issue. Keep the conventional `<type>(<scope>): <description>` format from CONTRIBUTING.md.
- We don't use the define and test one line `if` syntax, instead splitting definition and testing across two lines:

  ```go
  // Avoid
  if err := op(); err != nil {
      return err
  }

  // Prefer
  err := op()
  if err != nil {
      return err
  }
  ```

## Testing

This project's testing model differs from the org default, so the org
testing rules do not apply here. Follow this repo's own rules in
[CONTRIBUTING.md](CONTRIBUTING.md) and
[docs/architecture/testing.md](docs/architecture/testing.md). Use `just`
commands instead of raw `go` (see `just --list`).

## Working in this repo

- Check existing patterns in the codebase before creating new ones.
- Think through framework/library behavior before coding.
- Keep code direct - no unnecessary intermediate variables; use `_` for unused parameters.
- If cycling (same approach, no progress), stop and ask.

## Claude agents

Use `.claude/settings*.json` (permissions, deny list) and `.claude/commands/*`.
Run `/hello` to load full context.
