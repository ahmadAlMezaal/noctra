#!/usr/bin/env bash
# Test helper — shared setup for nightshift tests

set -euo pipefail

TESTS_PASSED=0
TESTS_FAILED=0
TESTS_TOTAL=0

# Source nightshift.sh in test mode (stops before entrypoint)
export NIGHTSHIFT_TESTING=true
export LINEAR_API_KEY="test-key"
export REPO_PATH="/tmp/nightshift-test-repo"
export MAIN_BRANCH="main"
export LOG_DIR="/tmp/nightshift-test-logs"
export WORKTREE_BASE="/tmp/nightshift-test-worktrees"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$SCRIPT_DIR/nightshift.sh"

# ── Assertions ────────────────────────────────────────────────────────────────

assert_equals() {
  local expected="$1"
  local actual="$2"
  local msg="${3:-"expected '$expected', got '$actual'"}"
  TESTS_TOTAL=$((TESTS_TOTAL + 1))
  if [ "$expected" = "$actual" ]; then
    echo "  PASS: $msg"
    TESTS_PASSED=$((TESTS_PASSED + 1))
  else
    echo "  FAIL: $msg"
    echo "    expected: '$expected'"
    echo "    actual:   '$actual'"
    TESTS_FAILED=$((TESTS_FAILED + 1))
  fi
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local msg="${3:-"output should contain '$needle'"}"
  TESTS_TOTAL=$((TESTS_TOTAL + 1))
  if echo "$haystack" | grep -qF -- "$needle"; then
    echo "  PASS: $msg"
    TESTS_PASSED=$((TESTS_PASSED + 1))
  else
    echo "  FAIL: $msg"
    echo "    needle:   '$needle'"
    echo "    haystack: '$haystack'"
    TESTS_FAILED=$((TESTS_FAILED + 1))
  fi
}

assert_not_contains() {
  local haystack="$1"
  local needle="$2"
  local msg="${3:-"output should NOT contain '$needle'"}"
  TESTS_TOTAL=$((TESTS_TOTAL + 1))
  if echo "$haystack" | grep -qF -- "$needle"; then
    echo "  FAIL: $msg"
    echo "    found:    '$needle'"
    echo "    haystack: '$haystack'"
    TESTS_FAILED=$((TESTS_FAILED + 1))
  else
    echo "  PASS: $msg"
    TESTS_PASSED=$((TESTS_PASSED + 1))
  fi
}

print_test_summary() {
  echo ""
  echo "Results: $TESTS_PASSED/$TESTS_TOTAL passed, $TESTS_FAILED failed"
  [ "$TESTS_FAILED" -eq 0 ]
}
