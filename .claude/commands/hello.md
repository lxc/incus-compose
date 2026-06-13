---
description: Initialize agent context for incus-compose. Load canonical rules, settings, and core architecture/testing docs. Lazy-load other docs on demand.
---

Load these files in parallel as your working context.

## Rules and settings (non-negotiable)

- `.rules` — AI-specific meta rules
- `.rules.local.md` — personal collaboration notes (treat as canonical for agent behavior; do not copy content into public docs)
- `CONTRIBUTING.md` — coding, architecture, testing, workflow rules
- `.claude/settings.json`, `.claude/settings.local.json` — permissions, deny list

## Canonical architecture and testing

Preload these — CONTRIBUTING.md cites them as authoritative:

- `docs/architecture.md`
- `docs/architecture/testing.md`

## Lazy-load on demand

Do NOT preload. Read only when the current task touches them.
