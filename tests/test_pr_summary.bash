#!/usr/bin/env bash
# Tests for PR summary extraction logic

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/test_helper.bash"

echo "=== PR Summary Tests ==="

# ── Setup ─────────────────────────────────────────────────────────────────────
TMPLOG=$(mktemp)
trap 'rm -f "$TMPLOG"' EXIT

# ── Test: DEBUG lines are stripped ────────────────────────────────────────────
echo "--- DEBUG line stripping ---"

cat > "$TMPLOG" <<'EOF'
--- Attempt 2024-01-01T00:00:00 ---
DEBUG: pwd = /repo
DEBUG: branch = nightshift/eng-42
Here is the summary of changes.
Added a new feature.
EOF

summary=$(awk '/^--- Attempt /{buf=""; next} {buf = buf $0 "\n"} END{printf "%s", buf}' "$TMPLOG" 2>/dev/null \
  | grep -v '^DEBUG: ' \
  | tail -n 40 || true)

assert_not_contains "$summary" "DEBUG:" "summary should not contain DEBUG lines"
assert_contains "$summary" "Added a new feature" "summary should contain actual content"

# ── Test: only content after last attempt marker is used ─────────────────────
echo "--- last attempt extraction ---"

cat > "$TMPLOG" <<'EOF'
--- Attempt 2024-01-01T00:00:00 ---
First attempt output that should be ignored.
--- Attempt 2024-01-01T01:00:00 ---
DEBUG: pwd = /repo
Second attempt — this is the real summary.
EOF

summary=$(awk '/^--- Attempt /{buf=""; next} {buf = buf $0 "\n"} END{printf "%s", buf}' "$TMPLOG" 2>/dev/null \
  | grep -v '^DEBUG: ' \
  | tail -n 40 || true)

assert_not_contains "$summary" "First attempt" "summary should not contain first attempt output"
assert_contains "$summary" "Second attempt" "summary should contain last attempt output"

# ── Test: attempt markers are not in the summary ─────────────────────────────
echo "--- attempt marker exclusion ---"

assert_not_contains "$summary" "--- Attempt" "summary should not contain attempt markers"

print_test_summary
