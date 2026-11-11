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
# Exclude repository instructions, reusable skills, hook configuration, this script, and build artifacts.

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

# Scan this repository's own code files: tracked ones and new ones that are
# not ignored. Other checkouts nested in the folder, such as worktrees, and
# ignored build output are not this repository's code.
git ls-files -z --cached --others --exclude-standard -- \
    '*.go' '*.sh' '*.yml' '*.yaml' '*.json' 'Dockerfile' '*/Dockerfile' \
    ':(exclude).agents/skills' \
    ':(exclude)scripts/check-anti-slop.sh' \
    ':(exclude).pre-commit-config.yaml' \
    ':(exclude).golangci.yml' \
    ':(exclude,glob)**/package-lock.json' \
    ':(exclude,glob)**/pnpm-lock.yaml' \
    | xargs -0 grep -inE "$REGEX_PATTERN" > "$GREP_OUT" 2>/dev/null || true

if [ -s "$GREP_OUT" ]; then
    echo "❌ [ANTI-SLOP VIOLATION] Lazy code patterns, placeholders, or stubs were detected:"
    echo "------------------------------------------------------------------------"
    cat "$GREP_OUT"
    echo "------------------------------------------------------------------------"
    echo "👉 Repository rules are defined in AGENTS.md."
    echo "👉 Assistants and developers must write complete, compiling, production-grade code."
    echo "👉 Please remove all placeholders and stubs before proceeding."
    exit 1
else
    echo "✅ Code hygiene check passed! Zero AI slop detected."
    exit 0
fi
