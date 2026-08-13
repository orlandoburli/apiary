package source

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// prSegments are the path segments a forge puts before a pull request's number:
// GitHub/Codeberg/Forgejo use "pull" (and "pulls" in API links), GitLab uses
// "merge_requests", Bitbucket "pull-requests".
var prSegments = map[string]bool{
	"pull":           true,
	"pulls":          true,
	"merge_requests": true,
	"pull-requests":  true,
}

// ParsePullRequestURL extracts the pull request number from a PR/MR browser
// URL. It is forge-agnostic on purpose: an agent that opened a PR reports the
// URL it got back, and the task it belongs to may come from a source (Jira,
// Plane) that has no idea what a pull request is.
//
// A trailing sub-path such as /files or /commits is tolerated, so a URL copied
// out of a browser tab still parses.
func ParsePullRequestURL(raw string) (PullRequestRef, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return PullRequestRef{}, fmt.Errorf("empty pull request URL")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return PullRequestRef{}, fmt.Errorf("invalid pull request URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return PullRequestRef{}, fmt.Errorf("pull request URL %q must be http(s)", raw)
	}

	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	// Walk from the end so the number is taken from the LAST pull segment: a
	// repository may itself be named "pull".
	for i := len(segments) - 1; i > 0; i-- {
		if !prSegments[segments[i-1]] {
			continue
		}
		n, err := strconv.Atoi(segments[i])
		if err != nil || n <= 0 {
			return PullRequestRef{}, fmt.Errorf("pull request URL %q has no valid number", raw)
		}
		return PullRequestRef{Number: n, URL: trimmed}, nil
	}
	return PullRequestRef{}, fmt.Errorf("pull request URL %q does not look like a pull request link", raw)
}
