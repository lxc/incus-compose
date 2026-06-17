---
name: issue
description: Initialize incus-compose context and plan a specific issue number or issue description before implementation.
---

# Plan a Specific Issue

Use this skill when the user invokes `/issue`, provides an issue number, or asks to plan work for a specific incus-compose issue.

## Steps

1. Load the `hello` skill context first, unless it has already been loaded in this session.
2. If the user provided an issue number, plan that issue.
3. If the user did not provide an issue number, title, or description, ask for it before continuing.
4. Do not implement code unless the user explicitly asks to proceed after the plan.

## Plan contents

Include:

- Issue goal and inferred scope.
- Relevant files and packages to inspect.
- Expected implementation approach.
- Tests or `just` commands to verify the change.
- Risks, unknowns, or questions to resolve before implementation.

## Notes

- Use `just` commands instead of raw `go` commands.
- Follow existing patterns before creating new ones.
