---
name: feedback
description: End an incus-compose work session by writing terse teammate-style collaboration feedback to a timestamped file in .feedback.
---

# Session Feedback

Use this skill when the user says the task is done, asks for feedback, invokes `/feedback`, or requests a session handoff focused on teamwork feedback.

Write terse feedback for Rene to a new markdown file under `.feedback/`.

## Steps

1. Treat the task as complete.
2. Give feedback on the teamwork between the user and the agent from the perspective of a human teammate.
3. Name yourself in the feedback.
4. Keep it terse and practical.
5. Create `.feedback/` if it does not exist.
6. Write the feedback to `.feedback/$(date +%Y%m%d-%H%M%S).md`, using the current local date and time for the filename.

## Notes

- `.feedback/` is gitignored and intended for personal notes.
- Do not copy private `.rules.local.md` content into public docs.
- If useful, include branch, uncommitted changes, active issue status, key context, and what persists in docs or code.
