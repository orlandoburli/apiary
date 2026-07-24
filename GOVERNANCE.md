# Apiary — Governance & Maintainership

## Current Maintainer

| Role | Person | Contact |
|------|--------|---------|
| Sole maintainer | Orlando Burli (`@orlandoburli`) | orlando.developermaster@gmail.com |

Apiary is a personal open-source project. There is currently **one maintainer** (bus-factor = 1).

---

## Accepted Risk: Single-Maintainer / No-SLA

This document records the formal governance decision required before any org-wide rollout
of Apiary (see [Security Rollout Issues](security-rollout-issues.md) — SEC-GOV).

**Decision (accepted 2026-07-24):** Apiary is adopted with full awareness of the
single-maintainer risk. The following constraints apply to all deployments:

### What this means for adopters

1. **No committed SLA.** There is no guaranteed response time for security patches,
   bug fixes, or feature requests. Maintainer availability is best-effort.

2. **Critical security findings.** If a critical vulnerability is reported (via a
   GitHub Security Advisory or a private e-mail to the maintainer), the target
   response is:
   - **Acknowledgement** within 5 business days.
   - **Patch or mitigation** within 30 calendar days.
   - If the maintainer is unreachable for more than 30 days with no public notice,
     adopting teams should treat the tool as unsupported and plan a migration or
     fork.

3. **Bus-factor mitigation.** Because there is no named backup maintainer at this
   time, adopting teams must:
   - Pin to a specific release tag (not `latest` or `main`) in any automated pipeline.
   - Maintain internal capacity to fork and apply emergency patches independently
     if a critical finding is unaddressed within the 30-day window above.
   - Track the upstream repository for activity signs (commits, responses to issues)
     as part of their vendor-risk review.

4. **Backup maintainer.** No backup maintainer has been named. If an internal team
   member is designated as backup owner for an org-wide deployment, that designation
   should be recorded in the internal adoption runbook for that deployment — it is an
   operational decision for each adopting org, not a commitment this repo can enforce.

---

## How to Report a Security Issue

Do **not** open a public GitHub issue for security vulnerabilities.

- **Preferred:** Open a [GitHub Security Advisory](https://github.com/orlandoburli/apiary/security/advisories/new) (private disclosure).
- **Fallback:** E-mail the maintainer directly at `orlando.developermaster@gmail.com`
  with subject `[APIARY SECURITY]`.

Include: affected version, reproduction steps, and potential impact. The maintainer
will acknowledge and coordinate a fix before any public disclosure.

---

## Versioning and Patch Policy

- Releases follow [Semantic Versioning](https://semver.org/).
- Security fixes are released as patch versions and back-ported to the most recent
  minor series when feasible.
- End-of-life versions receive no patches. Adopters on EOL versions should upgrade.

---

## Rollout Gate Reference

The org-wide rollout freeze documented in `security-rollout-issues.md` requires this
governance decision to be recorded before rollout is unblocked. Closing GitHub issue
[#247](https://github.com/orlandoburli/apiary/issues/247) signals that the decision
is on record and the rollout-gate condition for governance is met.
