#!/bin/bash
set -euo pipefail

# This script scans the codebase for common lazy code patterns, placeholders,
# and AI-generated stubs to enforce strict production-grade code hygiene.

echo "🔍 Running Anti-Slop & Code Hygiene checks..."

# Define forbidden case-insensitive patterns
FORBIDDEN_PATTERNS=(
    "\\bTODO\\b"
    "\\bFIXME\\b"
    "\\bHACK\\b"
    "\\bXXX\\b"
    "YOUR_CODE_HERE"
    "INSERT_CODE_HERE"
    "write code here"
    "implement me"
    "implement later"
    "placeholder"
    "stub"
    "rest of the code"
    "rest of the file"
    "\\.\\.\\. rest of"
    "//\\s*\\.\\.\\."
    "/\\*\\s*\\.\\.\\.\\s*\\*/"
    "//\\s*(Here you can|You can|We can|Here is where)"
    "//\\s*(Note:|As you can see)"
    "select\\s*\\{\\s*\\}"
    "//\\s*[A-Za-z0-9_]+\\s+does\\s+[a-z0-9_\\s]+"
    "\\b(mock|placeholder|dummy|temporary|temp)\\b\\s*[^a-zA-Z0-9]*\\s*(implementation|function|method|value|data|stub)"
)

# Files to check: Go, Shell scripts, Dockerfiles, YAML files, JSON files.
# Exclude: AI.md, AGENTS.md, .cursorrules, .pre-commit-config.yaml, this script, and build artifacts.

VIOLATIONS=0
GREP_OUT=$(mktemp)
trap 'rm -f "$GREP_OUT"' EXIT

# Combine patterns into a single regex for grep
REGEX_PATTERN=""
for p in "${FORBIDDEN_PATTERNS[@]}"; do
    if [ -n "$REGEX_PATTERN" ]; then
        REGEX_PATTERN="$REGEX_PATTERN|$p"
    else
        REGEX_PATTERN="$p"
    fi
done

# Find all code files and scan them
find . -type f \( \
    -name "*.go" -o \
    -name "*.sh" -o \
    -name "*.yml" -o \
    -name "*.yaml" -o \
    -name "Dockerfile" -o \
    -name "*.json" \
\) \
-not -path "*/.git/*" \
-not -path "*/.jules/*" \
-not -path "*/.gemini/*" \
-not -path "*/scripts/check-anti-slop.sh" \
-not -path "*/.pre-commit-config.yaml" \
-not -path "*/.cursorrules" \
-not -path "*/.golangci.yml" \
-not -name "package-lock.json" \
-not -name "go.sum" \
-print0 | xargs -0 grep -inE "$REGEX_PATTERN" > "$GREP_OUT" || true

if [ -s "$GREP_OUT" ]; then
    echo "❌ [ANTI-SLOP VIOLATION] Lazy code patterns, placeholders, or stubs were detected:"
    echo "------------------------------------------------------------------------"
    cat "$GREP_OUT"
    echo "------------------------------------------------------------------------"
    echo "👉 Anti-slop rules are defined in AI.md."
    echo "👉 AI Agents and developers must write complete, compiling, production-grade code."
    echo "👉 Please remove all placeholders and stubs before proceeding."
    exit 1
else
    echo "✅ Code hygiene check passed! Zero AI slop detected."
    exit 0
fi
