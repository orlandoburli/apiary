package improve

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// AppliedFile records one file written by an apply run.
type AppliedFile struct {
	Path           string
	Added, Removed int
	// MachineChecked mirrors the verdict: false for prose files, where the gate
	// could only confirm the patch applies.
	MachineChecked bool
}

// ApplyResult reports what an apply run changed.
type ApplyResult struct {
	Files []AppliedFile
	// ConfigChanged is true when any written file is one the daemon reads, so
	// the caller can print the restart reminder.
	ConfigChanged bool
	// VersionControlled is false when the workspace is not a git repository —
	// the one case where "git is the undo" does not hold.
	VersionControlled bool
}

// Apply writes accepted patches to disk.
//
// It deliberately does not back up, snapshot or offer to revert. The workspace
// is expected to be under version control, and `git diff` / `git checkout` do
// that job better than anything this command would reimplement. What it owes the
// operator instead is an accurate account of what it touched.
//
// Verdicts that failed the gate are skipped: reaching this function does not
// re-open the question of whether a patch is safe.
func Apply(verdicts []Verdict, root string) (*ApplyResult, error) {
	res := &ApplyResult{VersionControlled: IsGitRepo(root)}

	// Collisions matter: two accepted patches to the same file were each
	// validated against the ORIGINAL content, so applying both in sequence would
	// apply the second to content it never saw. Refuse rather than corrupt.
	seen := map[string]string{}
	for _, v := range verdicts {
		if !v.OK || v.Path == "" || strings.TrimSpace(v.Recommendation.Patch) == "" {
			continue
		}
		if prev, dup := seen[v.Path]; dup {
			return nil, fmt.Errorf("two proposals both patch %s (%s and %s); "+
				"each was validated against the original file, so applying both would be unsafe — "+
				"apply one, re-run, then consider the other",
				v.Path, prev, v.Recommendation.ID)
		}
		seen[v.Path] = v.Recommendation.ID
	}

	for _, v := range verdicts {
		if !v.OK || v.Path == "" || strings.TrimSpace(v.Recommendation.Patch) == "" {
			continue
		}

		// Preserve the file's existing permissions rather than imposing a mode.
		mode := os.FileMode(0o600)
		if st, err := os.Stat(v.Path); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.WriteFile(v.Path, []byte(v.Result), mode); err != nil {
			return res, fmt.Errorf("writing %s: %w", v.Path, err)
		}

		rel := v.Path
		if r, err := filepath.Rel(root, v.Path); err == nil {
			rel = r
		}
		res.Files = append(res.Files, AppliedFile{
			Path:           rel,
			Added:          v.Added,
			Removed:        v.Removed,
			MachineChecked: v.MachineChecked,
		})
		if v.MachineChecked {
			// Only config-like files are machine-checked, and those are exactly
			// the ones the daemon reads at startup.
			res.ConfigChanged = true
		}
	}

	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })
	return res, nil
}

// IsGitRepo reports whether dir is inside a git working tree.
func IsGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// Summary renders what an apply run did, plus the reminders that follow from it.
func (r *ApplyResult) Summary() string {
	if len(r.Files) == 0 {
		return "Nothing applied.\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Applied %d change(s):\n", len(r.Files))
	unchecked := 0
	for _, f := range r.Files {
		fmt.Fprintf(&b, "  %s  (+%d −%d)", f.Path, f.Added, f.Removed)
		if !f.MachineChecked {
			b.WriteString("  — prose, not machine-checked")
			unchecked++
		}
		b.WriteString("\n")
	}

	if !r.VersionControlled {
		// The command's whole undo story is "you have git". Where that is not
		// true, saying so is the least it can do — after the fact is still
		// better than never, and refusing to write would be worse.
		b.WriteString("\n⚠ This workspace is not a git repository, so there is no automatic way back.\n")
		b.WriteString("  These edits were written in place and are not recoverable from here.\n")
	} else {
		b.WriteString("\nReview with `git diff`; undo with `git checkout -- <file>`.\n")
	}

	if unchecked > 0 {
		fmt.Fprintf(&b, "\n%d of these are instruction files. Nothing about them could be validated\n", unchecked)
		b.WriteString("mechanically — read them before the next run picks them up.\n")
	}

	if r.ConfigChanged {
		b.WriteString("\nConfiguration changed. A running daemon keeps its loaded copy until restarted:\n")
		b.WriteString("  apiary restart\n")
	}
	return b.String()
}

// ConfirmationPrompt is the one-line question shown before writing, naming what
// is about to change so a blind "yes" is still an informed one.
func ConfirmationPrompt(verdicts []Verdict) string {
	var files []string
	unchecked := 0
	for _, v := range verdicts {
		if !v.OK || strings.TrimSpace(v.Recommendation.Patch) == "" {
			continue
		}
		files = append(files, v.Recommendation.File)
		if !v.MachineChecked {
			unchecked++
		}
	}
	if len(files) == 0 {
		return ""
	}
	sort.Strings(files)

	s := fmt.Sprintf("Apply %d change(s) to %s?", len(files), strings.Join(files, ", "))
	if unchecked > 0 {
		s += fmt.Sprintf(" (%d are instruction files that could not be validated)", unchecked)
	}
	return s + " [y/N] "
}
