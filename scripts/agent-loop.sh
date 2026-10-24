#!/bin/bash
# Runs the full verification gate. On failure it applies the safe automatic
# fixes (tidy, format, lint --fix) once and re-checks; anything still failing
# must be fixed by hand before the change is complete.
set -euo pipefail

if make check; then
    exit 0
fi
echo "⚠️  Checks failed; applying automatic fixes and re-checking..."
make fix
if make check; then
    echo "✅ Passed after automatic fixes. Review and commit the fixed files."
    exit 0
fi
echo "❌ Checks still fail. Fix the problems above and rerun 'make agent-fix'." >&2
exit 1
