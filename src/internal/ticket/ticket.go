// Package ticket recognizes human ticket-tracker references (Jira/Plane style
// keys such as PSP-348 or CDT-123) in free text.
//
// It exists because PR event tasks used to be created before anyone asked which
// ticket the PR was about, leaving "PR #800" rows that were event receipts
// rather than units of work. The dispatcher now resolves the ticket first; this
// is the single matcher both the GitHub adapter (which surfaces candidates) and
// its tests use.
package ticket

import "regexp"

// keyRe matches a tracker ticket key: an uppercase project key of 2-10
// characters (a letter followed by letters or digits), a hyphen, and a numeric
// suffix. Word boundaries keep it from matching inside a longer token.
//
// Only the canonical uppercase form is recognized. Lowercasing it would make
// ordinary branch fragments (feat/psp-348) indistinguishable from token pairs
// like "utf-8" or "http-2", turning almost any branch into a ticket reference.
var keyRe = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-[0-9]+\b`)

// Refs returns the ticket-key candidates in the order a PR event should try
// them: every key in title (left to right), then the head branch, then the
// body. Candidates are de-duplicated, keeping the first occurrence.
//
// The caller tries each candidate against the source bindings because a title
// can legitimately mention a non-ticket token that looks like a key before the
// real one ("Fix UTF-8 decoding for PSP-348"): the first candidate that is
// actually bound wins, and a false positive that is not bound is skipped rather
// than dropping the event.
//
// GitHub issue references (#42) are deliberately not keys — they are a
// different kind of reference and are resolved from the PR body separately
// (model.SourceEvent.RelatedItemID).
func Refs(title, branch, body string) []string {
	var refs []string
	seen := make(map[string]bool)
	for _, text := range []string{title, branch, body} {
		for _, ref := range keyRe.FindAllString(text, -1) {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs
}
