#!/usr/bin/env bash
# Tests for log_offset + tail -c pattern (critical regression tests)
#
# The log file appends across attempts. Without log_offset, BLOCKED/rate-limit
# from a previous attempt would be re-detected on subsequent attempts.

set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/test_helper.bash"

echo "=== Log Parsing Tests ==="

# ── Setup ─────────────────────────────────────────────────────────────────────
TMPLOG=$(mktemp)
trap 'rm -f "$TMPLOG"' EXIT

# ── Test: BLOCKED in old output is NOT detected ──────────────────────────────
echo "--- BLOCKED detection with log_offset ---"

# Simulate: first attempt wrote BLOCKED, second attempt is clean
cat > "$TMPLOG" <<'EOF'
--- Attempt 2024-01-01T00:00:00 ---
DEBUG: pwd = /repo
BLOCKED: Wrong repository
--- Attempt 2024-01-01T01:00:00 ---
DEBUG: pwd = /repo
All done, implementation complete.
EOF

# log_offset points to just before the second attempt
log_offset=$(echo -n "--- Attempt 2024-01-01T00:00:00 ---
DEBUG: pwd = /repo
BLOCKED: Wrong repository
" | wc -c)

current_output=$(tail -c +"$((log_offset + 1))" "$TMPLOG" 2>/dev/null || true)
blocked_line=$(echo "$current_output" | grep -i "^BLOCKED:" | head -1 || true)

assert_equals "" "$blocked_line" "BLOCKED in old output should NOT be detected"

# ── Test: BLOCKED in new output IS detected ──────────────────────────────────

cat > "$TMPLOG" <<'EOF'
--- Attempt 2024-01-01T00:00:00 ---
DEBUG: pwd = /repo
All good first time.
--- Attempt 2024-01-01T01:00:00 ---
DEBUG: pwd = /repo
BLOCKED: Missing API credentials
EOF

log_offset=$(echo -n "--- Attempt 2024-01-01T00:00:00 ---
DEBUG: pwd = /repo
All good first time.
" | wc -c)

current_output=$(tail -c +"$((log_offset + 1))" "$TMPLOG" 2>/dev/null || true)
blocked_line=$(echo "$current_output" | grep -i "^BLOCKED:" | head -1 || true)

assert_contains "$blocked_line" "BLOCKED: Missing API credentials" "BLOCKED in new output should be detected"

# ── Test: rate limit in old output is NOT detected ───────────────────────────
echo "--- Rate limit detection with log_offset ---"

cat > "$TMPLOG" <<'EOF'
--- Attempt 2024-01-01T00:00:00 ---
Error: rate limit exceeded
--- Attempt 2024-01-01T01:00:00 ---
Implementation complete.
EOF

log_offset=$(echo -n "--- Attempt 2024-01-01T00:00:00 ---
Error: rate limit exceeded
" | wc -c)

current_output=$(tail -c +"$((log_offset + 1))" "$TMPLOG" 2>/dev/null || true)
rate_hit=""
if echo "$current_output" | grep -qi "rate.limit\|usage.limit\|exceeded.*limit\|too many requests" 2>/dev/null; then
  rate_hit="yes"
fi

assert_equals "" "$rate_hit" "rate limit in old output should NOT be detected"

# ── Test: rate limit in new output IS detected ───────────────────────────────

cat > "$TMPLOG" <<'EOF'
--- Attempt 2024-01-01T00:00:00 ---
All good.
--- Attempt 2024-01-01T01:00:00 ---
Error: too many requests
EOF

log_offset=$(echo -n "--- Attempt 2024-01-01T00:00:00 ---
All good.
" | wc -c)

current_output=$(tail -c +"$((log_offset + 1))" "$TMPLOG" 2>/dev/null || true)
rate_hit=""
if echo "$current_output" | grep -qi "rate.limit\|usage.limit\|exceeded.*limit\|too many requests" 2>/dev/null; then
  rate_hit="yes"
fi

assert_equals "yes" "$rate_hit" "rate limit in new output should be detected"

print_test_summary
