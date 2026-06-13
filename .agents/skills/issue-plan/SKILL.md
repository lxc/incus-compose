---
name: issue-plan
description: Plan an incus-compose issue without implementing code, including files to inspect, approach, tests, risks, and questions.
---

# Plan an Issue

Use this skill when the user asks to plan an issue, invokes `/issue-plan`, or requests an implementation plan without code changes.

## Steps

1. Load the `hello` skill context first, unless it has already been loaded in this session.
2. If the user did not provide an issue number, issue title, or issue description, ask for it before continuing.
3. Do not implement code.
4. Inspect relevant existing patterns before proposing new ones.
5. Produce a concise plan.

## Plan contents

Include:

- Relevant files and packages to inspect.
- Expected implementation approach.
- Tests or `just` commands to verify the change.
- Risks, unknowns, or questions to resolve before implementation.

## Constraints

- Use `just` commands instead of raw `go` commands.
- Follow `CONTRIBUTING.md`, `docs/architecture.md`, and `docs/architecture/testing.md`.
- Keep the plan practical and direct.
