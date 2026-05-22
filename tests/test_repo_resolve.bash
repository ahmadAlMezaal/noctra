#!/usr/bin/env bash
# Tests for label-based multi-repo resolution

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/test_helper.bash"

echo "=== Repo Resolution Tests ==="

# ── repo_key_from_labels ─────────────────────────────────────────────────────
echo "--- repo_key_from_labels ---"

assert_equals "my-app" "$(repo_key_from_labels $'bug\nrepo:my-app\npriority')" \
  "extracts key from a repo: label among others"
assert_equals "my-app" "$(repo_key_from_labels 'Repo: my-app')" \
  "matches case-insensitively and trims surrounding space"
assert_equals "" "$(repo_key_from_labels $'bug\nfeature')" \
  "returns empty when there is no repo: label"

# ── resolve_repo: no label falls back to REPO_PATH ───────────────────────────
echo "--- resolve_repo fallback to REPO_PATH ---"

REPO_PATH="/tmp/some-default-repo"
MAIN_BRANCH="main"
REPOS_FILE="/tmp/nightshift-test-repos.json"
rm -f "$REPOS_FILE"

result=$(resolve_repo "ENG-1" "bug")
assert_equals $'/tmp/some-default-repo\tmain' "$result" \
  "no label resolves to REPO_PATH and MAIN_BRANCH"

# ── resolve_repo: labelled ticket but no repos file ──────────────────────────
echo "--- resolve_repo missing repos file ---"

if resolve_repo "ENG-2" "repo:ghost" 2>/dev/null; then
  failed="no"
else
  failed="yes"
fi
assert_equals "yes" "$failed" "labelled ticket fails when repos file is absent"

# ── resolve_repo: unknown key ────────────────────────────────────────────────
echo "--- resolve_repo unknown key ---"

cat > "$REPOS_FILE" <<'EOF'
{ "known": { "url": "/tmp/does-not-matter" } }
EOF

err=$(resolve_repo "ENG-3" "repo:unknown" 2>&1 || true)
assert_contains "$err" "not defined" "unknown repo key reports a clear error"

# ── resolve_repo: valid key clones on demand ─────────────────────────────────
echo "--- resolve_repo clone-on-demand ---"

SRC_REPO=$(make_test_tmpdir)
git -C "$SRC_REPO" init -b trunk --quiet
git -C "$SRC_REPO" config user.email "test@test.com"
git -C "$SRC_REPO" config user.name "Test"
git -C "$SRC_REPO" config commit.gpgsign false
echo "hello" > "$SRC_REPO/file.txt"
git -C "$SRC_REPO" add -A
git -C "$SRC_REPO" commit -m "init" --quiet

REPO_CACHE_BASE=$(make_test_tmpdir)
cat > "$REPOS_FILE" <<EOF
{ "app": { "url": "$SRC_REPO", "branch": "trunk" } }
EOF

result=$(resolve_repo "ENG-4" "repo:app")
resolved_path=$(printf '%s' "$result" | cut -f1)
resolved_branch=$(printf '%s' "$result" | cut -f2)

assert_equals "$REPO_CACHE_BASE/app" "$resolved_path" "valid key resolves to a cached clone path"
assert_equals "trunk" "$resolved_branch" "branch is read from the repos file"

if [ -d "$resolved_path/.git" ]; then clone_ok="yes"; else clone_ok="no"; fi
assert_equals "yes" "$clone_ok" "repo is cloned on demand"

# Second resolution reuses the existing clone (fetch path, not clone)
result2=$(resolve_repo "ENG-5" "repo:app")
assert_equals "$REPO_CACHE_BASE/app" "$(printf '%s' "$result2" | cut -f1)" \
  "second resolution reuses the cached clone"

# ── resolve_repo: branch defaults to MAIN_BRANCH when omitted ────────────────
echo "--- resolve_repo default branch ---"

cat > "$REPOS_FILE" <<EOF
{ "app2": { "url": "$SRC_REPO" } }
EOF

result=$(resolve_repo "ENG-6" "repo:app2")
assert_equals "main" "$(printf '%s' "$result" | cut -f2)" \
  "branch falls back to MAIN_BRANCH when not set in repos file"

rm -f "$REPOS_FILE"

print_test_summary
