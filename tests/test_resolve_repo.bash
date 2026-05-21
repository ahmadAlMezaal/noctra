#!/usr/bin/env bash
# Tests for the repo registry: slug generation and registry lookup.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/test_helper.bash"

echo "=== Repo Registry Tests ==="

# ── Test: repo_slug ──────────────────────────────────────────────────────────
echo "--- repo_slug ---"

assert_equals "auth-service" "$(repo_slug "Auth Service")" "spaces become dashes, lowercased"
assert_equals "my-api-v2" "$(repo_slug "My API   v2")" "repeated separators collapse to one"
assert_equals "nightshift" "$(repo_slug "Nightshift!")" "punctuation is stripped"

# ── Setup: a temporary registry ──────────────────────────────────────────────
REPOS_FILE=$(make_test_tmpfile)
cat > "$REPOS_FILE" <<'EOF'
{
  "repos": {
    "Auth Service": { "url": "git@github.com:me/auth.git", "main_branch": "develop" },
    "Web App": { "url": "https://github.com/me/web.git" }
  }
}
EOF

# ── Test: registry_lookup ────────────────────────────────────────────────────
echo "--- registry_lookup ---"

hit=$(registry_lookup "Auth Service" || true)
assert_contains "$hit" "git@github.com:me/auth.git" "lookup returns the repo url"
assert_contains "$hit" "develop" "lookup returns the configured main branch"

hit=$(registry_lookup "Web App" || true)
assert_contains "$hit" "$MAIN_BRANCH" "lookup falls back to the default main branch"

lookup_failed="no"
registry_lookup "Nonexistent Project" >/dev/null 2>&1 || lookup_failed="yes"
assert_equals "yes" "$lookup_failed" "lookup fails for an unregistered project"

lookup_failed="no"
registry_lookup "" >/dev/null 2>&1 || lookup_failed="yes"
assert_equals "yes" "$lookup_failed" "lookup fails for an empty project name"

print_test_summary
