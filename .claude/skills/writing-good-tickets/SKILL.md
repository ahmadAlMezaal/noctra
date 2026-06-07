---
name: writing-good-tickets
description: Use when drafting, reviewing, or improving Linear tickets so Nightshift can implement them autonomously with clear context, scope, acceptance criteria, and likely files.
---

# Writing Good Nightshift Tickets

Use this playbook when preparing a ticket for Nightshift or diagnosing a PR that missed the intended scope.

Nightshift hands the ticket text to a coding agent with no shared hallway context. Write the ticket as if a competent developer joined the team for one day and needs enough context to finish the work without asking follow-up questions.

## Ticket Template

```markdown
## Context
[What exists today and why this needs to change. Link to relevant code, errors, or docs.]

## Requirements
1. [Specific, implementable requirement]
2. [Another requirement]
3. [Continue as needed]

## Acceptance Criteria
- [ ] [Testable criterion that describes observable behavior]
- [ ] [Another criterion]
- [ ] All existing tests pass
- [ ] Linter passes with no new warnings

## Files likely involved
- `path/to/file` - [why this file matters]
- `path/to/another-file` - [why this file matters]

## Out of scope
- [Explicitly state what not to touch]
```

## Checklist

Before moving a ticket to the trigger state or adding the trigger label:

1. State the current behavior and the desired behavior.
2. Name the exact files, symbols, routes, commands, screenshots, logs, or docs that give context.
3. Include verbatim errors, stack traces, failed test output, or API responses for bug fixes.
4. Break requirements into concrete implementation steps, not broad outcomes.
5. Write acceptance criteria as observable checks a reviewer or test can verify.
6. Add test and lint expectations.
7. Call out what is out of scope to prevent unrelated refactors or polish.
8. Keep the ticket small enough for a single implementation session. A good target is work a mid-level developer could complete in 2-4 hours.

## Examples

Bad bug ticket:

```text
Fix the auth bug.
```

Good bug ticket:

```markdown
## Context
`POST /auth/login` returns 500 when a user's refresh token has expired.
It should return 401 and clear the session cookie so the client redirects
to login. `src/auth/auth.controller.ts` currently lets `TokenExpiredError`
escape from the login handler.

## Requirements
1. Catch `TokenExpiredError` from `jsonwebtoken` in the login handler.
2. Return HTTP 401 with body `{ "error": "session_expired" }`.
3. Clear the `refresh_token` cookie with `maxAge: 0`.

## Acceptance Criteria
- [ ] Expired refresh tokens return 401, not 500.
- [ ] The response clears the `refresh_token` cookie.
- [ ] A test covers the expired-token case.
- [ ] Existing auth tests pass.

## Files likely involved
- `src/auth/auth.controller.ts` - login handler.
- `src/auth/auth.controller.spec.ts` - add regression coverage.
```

Bad feature ticket:

```text
Add dark mode.
```

Good feature ticket:

```markdown
## Context
Users need a dark mode toggle. `src/contexts/ThemeContext.tsx` already
provides `theme` and `setTheme()`, and app startup already reads
AsyncStorage.

## Requirements
1. Add an Appearance section to `src/screens/SettingsScreen.tsx`.
2. Add a Light/Dark toggle that calls `setTheme()`.
3. Persist the selected value in AsyncStorage under `user:theme`.
4. Verify startup still defaults to the system theme when no preference exists.

## Acceptance Criteria
- [ ] Settings shows a light/dark toggle.
- [ ] The choice persists across app restarts.
- [ ] No preference still defaults to the system theme.
- [ ] Existing ThemeContext tests pass.

## Files likely involved
- `src/screens/SettingsScreen.tsx` - add the UI.
- `src/contexts/ThemeContext.tsx` - verify existing persistence behavior.

## Out of scope
- Do not update other screens.
- Do not add new theme tokens.
```

## If A Nightshift PR Missed The Mark

1. Check whether the ticket named the relevant files and observable acceptance criteria.
2. Add the missing context directly to the ticket or as a requeue note.
3. Close any incorrect PR if it is not salvageable.
4. Requeue the ticket after tightening scope.

