#!/usr/bin/env bash
# Tests for worktree creation and cleanup

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/test_helper.bash"

echo "=== Worktree Tests ==="

# ── Setup: create a temporary bare-ish repo to act as REPO_PATH ──────────────
TEST_REPO=$(make_test_tmpdir)
WORKTREE_BASE=$(make_test_tmpdir)

# Initialize a repo with at least one commit
git -C "$TEST_REPO" init -b main --quiet
git -C "$TEST_REPO" config user.email "test@test.com"
git -C "$TEST_REPO" config user.name "Test"
git -C "$TEST_REPO" config commit.gpgsign false
echo "init" > "$TEST_REPO/README.md"
git -C "$TEST_REPO" add -A
git -C "$TEST_REPO" commit -m "init" --quiet

# create_worktree needs origin/main, so set up a local remote
git -C "$TEST_REPO" remote add origin "$TEST_REPO" 2>/dev/null || true
git -C "$TEST_REPO" fetch origin --quiet 2>/dev/null || true

# Override globals for tests
REPO_PATH="$TEST_REPO"
MAIN_BRANCH="main"

# ── Test: create_worktree returns a clean path ───────────────────────────────
echo "--- create_worktree output ---"

result=$(create_worktree "ENG-100")
assert_contains "$result" "$WORKTREE_BASE/ENG-100" "worktree path should contain identifier"
assert_not_contains "$result" "Preparing" "output should not contain git noise"

# ── Test: worktree directory actually exists ─────────────────────────────────
echo "--- worktree directory existence ---"

if [ -d "$result" ]; then
  dir_exists="yes"
else
  dir_exists="no"
fi
assert_equals "yes" "$dir_exists" "worktree directory should exist after creation"

# Verify the branch is correct inside the worktree
branch_in_worktree=$(git -C "$result" branch --show-current 2>/dev/null)
assert_equals "nightshift/eng-100" "$branch_in_worktree" "worktree should be on the correct branch"

# ── Test: cleanup_worktree removes the worktree ─────────────────────────────
echo "--- cleanup_worktree ---"

cleanup_worktree "ENG-100"
if [ -d "$WORKTREE_BASE/ENG-100" ]; then
  cleanup_worked="no"
else
  cleanup_worked="yes"
fi
assert_equals "yes" "$cleanup_worked" "worktree directory should be removed after cleanup"

# ── Test: create_worktree fails gracefully on bad repo ───────────────────────
echo "--- create_worktree with bad repo ---"

REPO_PATH="/nonexistent/path"
result=""
if result=$(create_worktree "ENG-999" 2>/dev/null); then
  bad_repo_handled="no"
else
  bad_repo_handled="yes"
fi
assert_equals "yes" "$bad_repo_handled" "create_worktree should fail on bad repo path"

print_test_summary
