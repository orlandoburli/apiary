#!/usr/bin/env bash
# SEC-08 — Enable branch protection on main requiring human review.
#
# Run once by a repository admin:
#   GITHUB_TOKEN=<admin-pat> ./scripts/setup-branch-protection.sh
#
# Requirements:
#   - gh CLI authenticated as a repo admin
#   - Fine-grained token with Administration: Write (or classic with `repo`)
#
# What this script does:
#   1. Enables required pull request reviews (≥1 approving review from a
#      non-author reviewer) on the main branch.
#   2. Dismisses stale approvals on every new push so an injected commit
#      cannot ride an earlier human approval.
#   3. Does NOT enforce for administrators so maintainers can make emergency
#      fixes; revisit once sandboxed agent environments land (SEC-09+).
#
# This is idempotent — safe to re-run after any settings drift.

set -euo pipefail

REPO="${REPO:-orlandoburli/apiary}"
BRANCH="main"

echo "Applying branch protection to ${REPO}@${BRANCH} ..."

gh api \
  --method PUT \
  "/repos/${REPO}/branches/${BRANCH}/protection" \
  --input - <<'JSON'
{
  "required_status_checks": null,
  "enforce_admins": false,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": true,
    "require_code_owner_reviews": false,
    "required_approving_review_count": 1,
    "require_last_push_approval": false
  },
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "block_creations": false,
  "required_conversation_resolution": false,
  "lock_branch": false,
  "allow_fork_syncing": false
}
JSON

echo "Branch protection applied successfully."
echo ""
echo "Verify with:"
echo "  gh api repos/${REPO}/branches/${BRANCH}/protection | jq '.required_pull_request_reviews'"
