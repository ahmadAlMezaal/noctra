#!/usr/bin/env bash
# Run all nightshift tests

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TOTAL_SUITES=0
FAILED_SUITES=0

for test_file in "$SCRIPT_DIR"/test_*.bash; do
  [ -f "$test_file" ] || continue
  TOTAL_SUITES=$((TOTAL_SUITES + 1))
  echo ""
  echo "Running $(basename "$test_file")..."
  echo ""
  if bash "$test_file"; then
    echo "  >> SUITE PASSED"
  else
    echo "  >> SUITE FAILED"
    FAILED_SUITES=$((FAILED_SUITES + 1))
  fi
done

echo ""
echo "=============================="
echo "Test suites: $((TOTAL_SUITES - FAILED_SUITES))/$TOTAL_SUITES passed"
if [ "$FAILED_SUITES" -gt 0 ]; then
  echo "FAILED"
  exit 1
else
  echo "ALL PASSED"
fi
