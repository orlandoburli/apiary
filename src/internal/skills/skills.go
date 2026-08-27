// Package skills resolves the skill names declared in agents[].skills to files
// on disk.
//
// Apiary does not load skills itself — the names are passed through to the
// runner, which applies its own conventions. This package mirrors those
// conventions so that every surface that reports on skills (config validation,
// the daemon's startup check, the dashboard's agent panel and the improve
// advisor) agrees on where a skill is looked for and what it means when it is
// not there.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SearchDirs lists the directories a declared skill name may live in, relative
// to the workspace root and to the user's home directory. Order is significant:
// it is the order the candidate paths are tried and reported in.
var SearchDirs = []string{
	".claude/skills",
	".opencode/skills",
	".apiary/skills",
}

// Resolution is the outcome of resolving one declared skill name.
type Resolution struct {
	// Name is the skill name as declared in the config.
	Name string
	// Path is the SKILL.md that was found, or "" when nothing resolved.
	Path string
	// Candidates are every path tried, in order. Reported on failure so the
	// user can see the expected `<name>/SKILL.md` shape.
	Candidates []string
	// FlatFile is an existing `<name>.md` sitting where `<name>/SKILL.md` was
	// expected. That is the mistake users actually make — souls are configured
	// as plain `.md` paths and skills are not — so it earns its own hint.
	FlatFile string
}

// Found reports whether the skill resolved to a file on disk.
func (r Resolution) Found() bool { return r.Path != "" }

// Reason renders the failure as a single human-readable clause, listing every
// candidate path tried and, when a flat `<name>.md` exists, the directory form
// the user probably meant. It returns "" for a resolved skill.
func (r Resolution) Reason() string {
	if r.Found() {
		return ""
	}
	tried := make([]string, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		tried = append(tried, display(c))
	}
	msg := fmt.Sprintf("skill %q not found (tried %s)", r.Name, strings.Join(tried, ", "))
	if r.FlatFile != "" {
		msg += fmt.Sprintf("; found %s — did you mean %s?",
			display(r.FlatFile), display(expectedFor(r.FlatFile, r.Name)))
	}
	return msg
}

// expectedFor maps a flat `<dir>/<name>.md` to the `<dir>/<name>/SKILL.md` it
// should have been.
func expectedFor(flat, name string) string {
	return filepath.Join(filepath.Dir(flat), name, "SKILL.md")
}

// display shortens a path under the user's home to `~/…` so the candidate list
// stays readable.
func display(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Join("~", rel)
	}
	return path
}

// roots returns the directories the search dirs are joined onto: the workspace
// root first (an empty root means the current working directory), then the
// user's home, where globally installed skills live.
func roots(root string) []string {
	out := []string{root}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != root {
		out = append(out, home)
	}
	return out
}

// Candidates returns every path a skill named `name` is looked for, in order.
// Only the `<name>/SKILL.md` form is a candidate: a flat `<name>.md` is not
// loaded by any runner, so treating it as a resolution would reproduce exactly
// the silent failure this list exists to expose.
func Candidates(root, name string) []string {
	if name == "" {
		return nil
	}
	var out []string
	for _, r := range roots(root) {
		for _, dir := range SearchDirs {
			out = append(out, filepath.Join(r, filepath.FromSlash(dir), name, "SKILL.md"))
		}
	}
	return out
}

// Resolve looks for a declared skill name under root and the user's home.
func Resolve(root, name string) Resolution {
	res := Resolution{Name: name, Candidates: Candidates(root, name)}
	if name == "" {
		return res
	}
	for _, candidate := range res.Candidates {
		if isFile(candidate) {
			res.Path = candidate
			return res
		}
	}
	for _, r := range roots(root) {
		for _, dir := range SearchDirs {
			flat := filepath.Join(r, filepath.FromSlash(dir), name+".md")
			if isFile(flat) {
				res.FlatFile = flat
				return res
			}
		}
	}
	return res
}

// PrimaryPath is the path a skill is expected at when nothing resolved: the
// first candidate. Surfaces that must show a path even for a missing skill (the
// dashboard's agent files list) use it.
func PrimaryPath(root, name string) string {
	if c := Candidates(root, name); len(c) > 0 {
		return c[0]
	}
	return ""
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
