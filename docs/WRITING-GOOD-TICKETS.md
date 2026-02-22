# Writing Good Tickets for Nightshift

Nightshift is only as good as your tickets.

Claude has no memory of previous sessions, no awareness of verbal discussions, and no ability to make judgment calls about ambiguous scope. It will implement exactly what the ticket says — no more, no less. If the ticket is vague, the output will be vague.

The good news: writing agent-friendly tickets is the same as writing good tickets for a junior developer joining your team for a single day. Context, clarity, and scope are everything.

---

## The Template

Copy this into every ticket you want Nightshift to pick up:

```markdown
## Context
[What exists today and why this needs to change. Link to relevant code, errors, or docs.]

## Requirements
1. [Specific, implementable requirement]
2. [Another requirement]
3. [Continue as needed]

## Acceptance Criteria
- [ ] [Testable criterion — describes observable behavior, not implementation]
- [ ] [Another criterion]
- [ ] All existing tests pass
- [ ] Linter passes (no new warnings)

## Files likely involved
- `src/path/to/relevant/file.ts` — [why this file]
- `src/path/to/another/file.ts` — [why this file]

## Out of scope
- [Explicitly state what NOT to touch — prevents Claude from gold-plating]
```

---

## Good vs. Bad: Side by Side

### Bug fixes

❌ **Bad:**
```
Fix the auth bug
```

✅ **Good:**
```
## Context
The login endpoint (`POST /auth/login`) returns a 500 error when a user's
refresh token has expired. It should return 401 and clear the session cookie
so the client can redirect to the login page. Currently line 42 of
`src/auth/auth.controller.ts` throws an unhandled `TokenExpiredError`.

## Requirements
1. Catch `TokenExpiredError` from `jsonwebtoken` in the login handler
2. Return HTTP 401 with body `{ "error": "session_expired" }`
3. Clear the `refresh_token` cookie in the response (set maxAge: 0)

## Acceptance Criteria
- [ ] POST /auth/login with expired refresh token returns 401 (not 500)
- [ ] Response clears the refresh_token cookie
- [ ] All existing tests in `auth.controller.spec.ts` still pass
- [ ] New test covers the expired token case

## Files likely involved
- `src/auth/auth.controller.ts` — contains the login handler (line 42)
- `src/auth/auth.controller.spec.ts` — add new test case here
```

---

### New features

❌ **Bad:**
```
Add dark mode
```

✅ **Good:**
```
## Context
Users have requested a dark mode toggle. We have a ThemeContext at
`src/contexts/ThemeContext.tsx` that provides `theme` ("light" | "dark")
and `setTheme()`. We need to add a toggle in the Settings screen and
persist the preference.

## Requirements
1. Add a "Appearance" section to `src/screens/SettingsScreen.tsx`
   with a toggle switch between Light / Dark
2. On toggle, call `setTheme()` from ThemeContext
3. Persist the user's choice in AsyncStorage under the key `"user:theme"`
4. On app launch, load the persisted preference (already done in
   ThemeContext — just verify it reads from AsyncStorage)
5. Default to system theme if no preference is stored (system preference
   detection is already in ThemeContext)

## Acceptance Criteria
- [ ] Settings screen shows a light/dark toggle
- [ ] Toggling persists across app restarts
- [ ] Default is system theme when no preference is stored
- [ ] Existing ThemeContext tests pass
- [ ] No TypeScript errors

## Files likely involved
- `src/screens/SettingsScreen.tsx` — add the toggle UI
- `src/contexts/ThemeContext.tsx` — read-only, verify AsyncStorage integration

## Out of scope
- Don't update any other screens — only Settings gets the toggle for now
- Don't change the ThemeContext implementation
- Don't add new theme tokens or colors — use what exists
```

---

### Refactors

❌ **Bad:**
```
Clean up the user service
```

✅ **Good:**
```
## Context
`src/services/UserService.ts` has grown to 800 lines and contains both
database queries and business logic mixed together. We want to extract
the database access into a separate `UserRepository` class.

## Requirements
1. Create `src/repositories/UserRepository.ts` with methods:
   - `findById(id: string): Promise<User | null>`
   - `findByEmail(email: string): Promise<User | null>`
   - `create(data: CreateUserInput): Promise<User>`
   - `update(id: string, data: Partial<User>): Promise<User>`
   - `delete(id: string): Promise<void>`
2. Move the raw Prisma calls from UserService into UserRepository
3. Update UserService to use UserRepository via constructor injection
4. Update `src/modules/user.module.ts` to provide UserRepository

## Acceptance Criteria
- [ ] All existing UserService tests still pass without modification
- [ ] UserService no longer imports Prisma directly
- [ ] UserRepository has its own test file with basic CRUD tests
- [ ] No behavior changes — this is a pure refactor

## Files likely involved
- `src/services/UserService.ts` — remove Prisma imports, use repo
- `src/repositories/UserRepository.ts` — create this file
- `src/modules/user.module.ts` — add UserRepository to providers
- `src/services/__tests__/UserService.spec.ts` — should pass unchanged

## Out of scope
- Don't refactor any other services
- Don't add new UserService methods
- Don't change the Prisma schema
```

---

## Tips for Writing Agent-Friendly Tickets

### 1. Mention specific files

The single biggest quality improvement. Claude reads files to understand context. Pointing to exact files saves 10–20 minutes of exploration and dramatically reduces the chance of changes landing in the wrong place.

```
✅ "See src/auth/auth.controller.ts line 42"
❌ "See the auth controller"
```

### 2. Include error messages verbatim

If you're fixing a bug triggered by an error, paste the full stack trace or error message. Claude can find the exact source instantly.

```
✅ "Throws: TypeError: Cannot read property 'userId' of undefined
       at UserService.getProfile (src/services/UserService.ts:156)"
❌ "It throws an error when fetching the profile"
```

### 3. Link to relevant code or docs

Linear supports markdown. Link to specific files, PRs, docs pages, or external references. Claude will read them.

```
✅ "API docs: https://stripe.com/docs/api/payment_intents/create"
✅ "See the existing implementation in ENG-38 for the pattern to follow"
```

### 4. Keep scope to one context window

Claude works best when a ticket can be fully understood and implemented in a single session. If a feature spans 10+ files and 3 days of work, break it into smaller tickets.

**A good size:** something a mid-level developer could implement in 2–4 hours.

### 5. Write acceptance criteria as tests

Good acceptance criteria describe *observable behavior* that can be verified by running tests or checking the UI. Avoid criteria like "code is clean" or "it works" — these are unmeasurable.

```
✅ "POST /users with duplicate email returns 409"
✅ "All existing tests in auth.spec.ts pass"
❌ "The auth code is refactored"
❌ "It handles errors properly"
```

### 6. Explicitly state what's out of scope

Claude will sometimes add "helpful" improvements that weren't asked for. Prevent scope creep by explicitly stating what not to touch.

```
✅ "Out of scope: don't update any other screens — only Settings for now"
✅ "Out of scope: don't change the API contract"
```

### 7. Break big features into small tickets

One ticket per context. A feature like "Add OAuth login" should become:

- `ENG-50`: Add OAuth provider configuration to auth module
- `ENG-51`: Add Google OAuth callback handler and session creation
- `ENG-52`: Add OAuth login button to the login screen
- `ENG-53`: Add tests for OAuth flow

Each ticket is self-contained, independently implementable, and reviewable.

---

## The Test: Would a New Dev Understand This?

Before queuing a ticket for Nightshift, ask: *if I handed this ticket to a competent developer who just joined the team today and had no context from our Slack conversations, would they know exactly what to implement?*

If the answer is no, add more context. The extra 5 minutes of ticket writing will save you from reviewing and reverting a PR that missed the point.
