#!/bin/bash
set -euo pipefail

# Secure Jules PR Automation and Auto-Merge Script
#
# This script is called by GitHub Actions when a CI run completes.
# It enforces Multi-Factor Security Verification to ensure that only
# authentic PRs created by Google Labs Jules can be auto-merged or commented on.
#
# Usage: ./scripts/jules-automation.sh <run_id> <conclusion> <head_branch> <head_sha> <is_fork>

if [ "$#" -ne 5 ]; then
    echo "❌ Error: Missing arguments."
    echo "Usage: $0 <run_id> <conclusion> <head_branch> <head_sha> <is_fork>"
    exit 1
fi

RUN_ID="$1"
CONCLUSION="$2"
HEAD_BRANCH="$3"
HEAD_SHA="$4"
IS_FORK="$5"

echo "🤖 Starting Secure Jules PR Automation..."
echo "Parameters:"
echo "  Run ID: $RUN_ID"
echo "  Conclusion: $CONCLUSION"
echo "  Head Branch: $HEAD_BRANCH"
echo "  Head SHA: $HEAD_SHA"
echo "  Is Fork: $IS_FORK"

# 1. Security Check: Block all forks to prevent supply chain attacks
if [ "$IS_FORK" = "true" ]; then
    echo "🛡️ Security Check Failed: Pull request originates from a fork. Disabling automation to prevent supply chain attacks."
    exit 0
fi

# 2. Fetch the corresponding open pull request
echo "🔍 Searching for open pull request for commit $HEAD_SHA or branch $HEAD_BRANCH..."
pr_json=$(gh pr list --commit "$HEAD_SHA" --state open --json number,title,body,headRefName,headRefOid 2>/dev/null || echo "[]")

# If no PR found by commit, fallback to branch name
if [ "$pr_json" = "[]" ] || [ "$(echo "$pr_json" | jq '. | length')" -eq 0 ]; then
    pr_json=$(gh pr list --head "$HEAD_BRANCH" --state open --json number,title,body,headRefName,headRefOid 2>/dev/null || echo "[]")
fi

if [ "$pr_json" = "[]" ] || [ "$(echo "$pr_json" | jq '. | length')" -eq 0 ]; then
    echo "ℹ️ No open pull request found matching commit or branch. Skipping."
    exit 0
fi

PR_NUMBER=$(echo "$pr_json" | jq -r '.[0].number')
PR_TITLE=$(echo "$pr_json" | jq -r '.[0].title')
PR_BODY=$(echo "$pr_json" | jq -r '.[0].body')
PR_BRANCH=$(echo "$pr_json" | jq -r '.[0].headRefName')
PR_HEAD_SHA=$(echo "$pr_json" | jq -r '.[0].headRefOid')

echo "Found PR #$PR_NUMBER:"
echo "  Title: $PR_TITLE"
echo "  Branch: $PR_BRANCH"
echo "  Branch HEAD SHA: $PR_HEAD_SHA"

# 3. Security Check: Verify that the CI-tested SHA matches the current PR branch HEAD SHA
# This completely blocks "race condition" or "split-second" branch updates pushed during a CI run.
if [ "$HEAD_SHA" != "$PR_HEAD_SHA" ]; then
    echo "🛡️ Security Check Failed: Tested SHA ($HEAD_SHA) does not match current branch HEAD ($PR_HEAD_SHA)."
    echo "  Aborting to prevent unauthorized merges of untested commits."
    exit 0
fi

# 4. Security Check: Verify Jules branch signature
# Jules branches are prefixed or postfixed with tool names: bolt, chisel, keystone, scribe, cargo, tracer, hound, sentinel, palette
is_jules_branch=false
if [[ "$PR_BRANCH" =~ ^(bolt|chisel|keystone|scribe|cargo|tracer|hound|sentinel|palette)/ ]] || \
   [[ "$PR_BRANCH" =~ ^(bolt|chisel|keystone|scribe|cargo|tracer|hound|sentinel|palette)- ]]; then
    is_jules_branch=true
fi

# 5. Security Check: Verify Jules description signature
is_jules_desc=false
if [[ "$PR_BODY" == *"PR created automatically by Jules"* ]] || \
   [[ "$PR_TITLE" =~ ^(⚡|🛠️|✍️|🧱|📦|🧪|🐛|🛡️)\ (Bolt|Chisel|Scribe|Keystone|Cargo|Tracer|Hound|Sentinel|Palette): ]]; then
    is_jules_desc=true
fi

# 6. Security Check: Verify official comment and commit authorship by @google-labs-jules / google-labs-jules[bot]
echo "🛡️ Verifying PR authorship, verified comments, and commit signatures..."
pr_details=$(gh pr view "$PR_NUMBER" --json comments,commits)
has_verified_comment=$(echo "$pr_details" | jq '[.comments[] | select(.author.login == "google-labs-jules")] | length')
has_verified_commit=$(echo "$pr_details" | jq '[.commits[].authors[] | select(.login == "google-labs-jules[bot]")] | length')

# 7. Security Check: Whitelist authorized committers (Only MikeO7 and google-labs-jules[bot] are permitted on this branch)
# This completely blocks split-commit injections or any unauthorized commits pushed by third parties to a direct branch.
unauthorized_commits=$(echo "$pr_details" | jq '[.commits[].authors[] | select(.login != "google-labs-jules[bot]" and .login != "MikeO7")] | length')

if [ "$is_jules_branch" = "false" ] || [ "$is_jules_desc" = "false" ] || [ "$has_verified_comment" -eq 0 ] || [ "$has_verified_commit" -eq 0 ] || [ "$unauthorized_commits" -gt 0 ]; then
    echo "🛡️ Security Check Failed: This pull request does not meet the strict signature verification criteria:"
    echo "  - Matches branch signature: $is_jules_branch"
    echo "  - Matches description signature: $is_jules_desc"
    echo "  - Has verified comment from @google-labs-jules: $((has_verified_comment > 0 ? 1 : 0))"
    echo "  - Has verified commit from google-labs-jules[bot]: $((has_verified_commit > 0 ? 1 : 0))"
    echo "  - Contains unauthorized committers: $((unauthorized_commits > 0 ? 1 : 0)) (Count: $unauthorized_commits)"
    echo "  Aborting automation to prevent unauthorized merges."
    exit 0
fi

echo "✅ All Security Checks Passed! Authenticity of Jules PR verified."

# 6. Process conclusion
if [ "$CONCLUSION" = "success" ]; then
    echo "🎉 CI/CD Pipeline Passed! Attempting auto-merge..."
    if gh pr merge "$PR_NUMBER" --squash --delete-branch -y; then
        echo "✅ Pull request #$PR_NUMBER successfully merged!"
    else
        echo "⚠️ Failed to auto-merge pull request #$PR_NUMBER. It may require manual intervention or conflict resolution."
    fi
elif [ "$CONCLUSION" = "failure" ]; then
    echo "🚨 CI/CD Pipeline Failed! Fetching failure logs..."

    # Fetch failed logs (limit to 100 lines/8000 characters to prevent huge payloads)
    raw_logs=$(gh run view "$RUN_ID" --log-failed 2>/dev/null || echo "No failed log details available.")

    # Strip ANSI escape codes to ensure clean markdown
    clean_logs=$(echo "$raw_logs" | sed 's/\x1b\[[0-9;]*[a-zA-Z]//g' | head -n 120)

    # Build comment body
    COMMENT_BODY=$(cat <<EOF
🚨 **CI/CD Pipeline Failed!** @google-labs-jules @jules

The automated verification checks failed for commit \`${HEAD_SHA}\`. Here is a summary of the failure details:

\`\`\`text
${clean_logs}
\`\`\`

Please review these failures, perform the necessary adjustments, and push an updated commit to resolve the pipeline issues.
EOF
)

    echo "💬 Posting failure summary and tagging Jules on PR #$PR_NUMBER..."
    if gh pr comment "$PR_NUMBER" --body "$COMMENT_BODY"; then
        echo "✅ Successfully commented on PR #$PR_NUMBER."
    else
        echo "❌ Failed to comment on PR #$PR_NUMBER."
    fi
else
    echo "ℹ️ Workflow run ended with status '$CONCLUSION'. No action taken."
fi
