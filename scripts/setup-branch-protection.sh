#!/usr/bin/env bash
# setup-branch-protection.sh — enforce human review gate on main
#
# Configures GitHub branch protection on the main branch of orlandoburli/apiary
# so that NO pull request (including agent-authored ones) can merge without at
# least one human approving review and all required status checks passing.
#
# Prerequisites: gh CLI authenticated with a token that has admin repo access.
#
# Usage:
#   GITHUB_TOKEN=<token> ./scripts/setup-branch-protection.sh
#   # or just: ./scripts/setup-branch-protection.sh  (uses ambient GH_TOKEN/GITHUB_TOKEN)
set -euo pipefail

REPO="orlandoburli/apiary"
BRANCH="main"

echo "Configuring branch protection for ${REPO}@${BRANCH}..."

gh api   --method PUT   "/repos/${REPO}/branches/${BRANCH}/protection"   --field "required_status_checks[strict]=true"   --field "required_status_checks[contexts][]=CI"   --field "required_pull_request_reviews[required_approving_review_count]=1"   --field "required_pull_request_reviews[dismiss_stale_reviews]=true"   --field "required_pull_request_reviews[require_code_owner_reviews]=false"   --field "required_pull_request_reviews[restrict_dismissals]=false"   --field "enforce_admins=false"   --field "allow_force_pushes=false"   --field "allow_deletions=false"   --field "block_creations=false"   --field "required_conversation_resolution=false"

echo "Branch protection applied:"
echo "  - Required approving reviews: 1"
echo "  - Dismiss stale reviews: true"
echo "  - Required status checks: CI"
echo "  - Strict status checks (branch must be up-to-date): true"
echo "  - Force pushes: disabled"
echo ""
echo "Agent-authored PRs now require a human approval before they can merge."
