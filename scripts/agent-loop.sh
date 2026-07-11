#!/bin/bash
set -euo pipefail

# This script runs the validation checks, attempts to auto-fix minor issues,
# and guides AI agents or developers to iterate until all issues are resolved.

echo "🔄 Running Agent Self-Correction & Verification Loop..."

# Run baseline checks
if make check; then
    echo "✅ [SUCCESS] All checks passed on the first run! Code is pristine."
    exit 0
fi

echo "⚠️  [WARNING] Checks failed. Attempting to automatically fix format and lint issues..."
make fix

echo "🔄 Re-running verification checks..."
# Create a temp file to capture the output of make check
CHECK_LOG=$(mktemp)
trap 'rm -f "$CHECK_LOG"' EXIT

# Run check again, streaming output to stdout and capturing it in CHECK_LOG
if make check 2>&1 | tee "$CHECK_LOG"; then
    echo "✅ [SUCCESS] Verification passed after auto-fixes! Code is clean."
    exit 0
fi

# If it still fails, print a structured instruction block for the AI agent
echo ""
echo "========================================================================"
echo "⚠️  [AGENT ACTION REQUIRED] CODE HYGIENE OR TEST FAILURES REMAIN ⚠️"
echo "------------------------------------------------------------------------"
echo "The verification pipeline is still failing after auto-fixes."
echo "As an AI Agent / developer, you are REQUIRED to:"
echo "  1. Review the failure details below."
echo "  2. Modify the code to fix compile errors, test failures, or lint issues."
echo "  3. Re-run 'make agent-fix' to verify your changes."
echo "  4. DO NOT mark this task complete or finish your turn until this exits 0."
echo "------------------------------------------------------------------------"
echo "📋 REMAINING FAILURES:"
echo "------------------------------------------------------------------------"
cat "$CHECK_LOG"
echo "------------------------------------------------------------------------"
echo "========================================================================"
exit 1
