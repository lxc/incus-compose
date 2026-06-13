---
name: hello
description: Initialize agent context for incus-compose by loading canonical rules, settings, and core architecture/testing docs; lazy-load other docs on demand.
---

# Initialize incus-compose Agent Context

Use this skill when starting work in the `incus-compose` repository or when the user asks to initialize, refresh, or load agent context.

Load the following files in parallel as working context before making code or workflow decisions.

## Rules and settings

Treat these as non-negotiable for agent behavior:

- `.rules` - AI-specific meta rules
- `.rules.local.md` - personal collaboration notes; treat as canonical for agent behavior, but do not copy its content into public docs
- `CONTRIBUTING.md` - coding, architecture, testing, and workflow rules
- `.claude/settings.json` - permissions and deny list
- `.claude/settings.local.json` - local permissions and deny list

## Canonical architecture and testing

Preload these because `CONTRIBUTING.md` cites them as authoritative:

- `docs/architecture.md`
- `docs/architecture/testing.md` - use `just test` instead `just test-local`.

## Lazy-load on demand

Do not preload additional docs. Read other project documents only when the current task touches them.
