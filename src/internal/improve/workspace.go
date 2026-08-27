package improve

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/skills"
)

// FileKind classifies a workspace file by what changing it would affect.
type FileKind string

const (
	KindConfig   FileKind = "config"
	KindWorkflow FileKind = "workflow"
	KindSoul     FileKind = "soul"
	KindSkill    FileKind = "skill"
)

// WorkspaceFile is one file the advisor may read and propose edits to.
type WorkspaceFile struct {
	Path    string   `json:"path"`
	Kind    FileKind `json:"kind"`
	Owner   string   `json:"owner,omitempty"` // agent or workflow the file belongs to
	Content string   `json:"-"`
	Bytes   int      `json:"bytes"`
}

// Workspace is the complete set of files that shape agent behaviour.
type Workspace struct {
	Root  string          `json:"root"`
	Files []WorkspaceFile `json:"files"`
	// UnresolvedSkills are skills declared by an agent that could not be located
	// on disk. They are reported rather than dropped: an advisor reasoning about
	// an agent whose instructions it never saw is worse than one that knows a
	// piece is missing.
	UnresolvedSkills []string `json:"unresolved_skills,omitempty"`
}

// Discover walks the configuration to find every file that shapes agent
// behaviour: the config itself, referenced workflow files, soul files and
// resolvable skill definitions.
func Discover(cfg *config.Config, configFile string) (*Workspace, error) {
	if cfg == nil {
		return nil, fmt.Errorf("no configuration loaded")
	}
	abs, err := filepath.Abs(configFile)
	if err != nil {
		abs = configFile
	}
	root := filepath.Dir(abs)
	if filepath.Base(root) == ".apiary" {
		root = filepath.Dir(root)
	}

	ws := &Workspace{Root: root}
	seen := map[string]bool{}

	add := func(path string, kind FileKind, owner string) {
		if path == "" {
			return
		}
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(root, path)
		}
		full = filepath.Clean(full)
		if seen[full] || Excluded(full, root) {
			return
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return // referenced but absent; cfg.Validate reports soul files properly
		}
		seen[full] = true
		content := string(data)
		if kind == KindConfig {
			content = RedactConfig(content)
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			rel = full
		}
		ws.Files = append(ws.Files, WorkspaceFile{
			Path: rel, Kind: kind, Owner: owner, Content: content, Bytes: len(data),
		})
	}

	add(abs, KindConfig, "")

	for _, wf := range cfg.Workflows {
		for _, s := range wf.Steps {
			if s.Uses != "" {
				// `uses` is relative to the declaring YAML, not the root.
				add(filepath.Join(filepath.Dir(abs), s.Uses), KindWorkflow, wf.ID)
			}
		}
	}

	for _, a := range cfg.Agents {
		add(a.SoulFile, KindSoul, a.ID)
		for _, skill := range a.Skills {
			if path := resolveSkill(root, skill); path != "" {
				add(path, KindSkill, a.ID)
			} else {
				ws.UnresolvedSkills = append(ws.UnresolvedSkills,
					fmt.Sprintf("%s (declared by %s)", skill, a.ID))
			}
		}
	}

	sort.Strings(ws.UnresolvedSkills)
	ws.UnresolvedSkills = dedupe(ws.UnresolvedSkills)
	return ws, nil
}

// resolveSkill looks for a declared skill name in the conventional locations.
// It delegates to the shared resolver so the advisor, `apiary validate`, the
// daemon's startup warning and the dashboard all search the same paths.
func resolveSkill(root, name string) string {
	return skills.Resolve(root, name).Path
}

// excludedNames matches files that must never reach a prompt or a patch,
// regardless of where they sit.
var excludedNames = regexp.MustCompile(`(^|/)(\.env(\..*)?|.*\.env|.*secret.*|.*credential.*|.*\.pem|.*\.key)$`)

// excludedDirs are directories whose contents are never part of the workspace.
var excludedDirs = []string{".git/", "/logs/", "/transcripts/", "/memory/", "node_modules/"}

// Excluded reports whether a path must be kept out of the workspace: secrets,
// version-control internals, and the runtime data directories. It also rejects
// anything outside the workspace root, so a patch cannot escape upward.
func Excluded(path, root string) bool {
	clean := filepath.Clean(path)
	slashed := filepath.ToSlash(clean)

	if root != "" {
		rel, err := filepath.Rel(root, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return true
		}
	}
	if excludedNames.MatchString(slashed) {
		return true
	}
	for _, d := range excludedDirs {
		if strings.Contains(slashed+"/", d) {
			return true
		}
	}
	// The database and its WAL sidecars.
	if ext := filepath.Ext(slashed); ext == ".db" || ext == ".db-wal" || ext == ".db-shm" || ext == ".sock" {
		return true
	}
	return false
}

// secretKeys are config keys whose values are blanked before the config text
// enters a prompt. The advisor never needs a token's value to reason about
// configuration, and a prompt is the last place a credential should appear.
var secretKeys = regexp.MustCompile(`(?i)^(\s*(?:-\s*)?)(source_token|token|api_key|apikey|password|secret|.*_token|.*_key)(\s*:\s*)(.+)$`)

// envValueLine matches an indented `KEY: value` pair, used to blank the bodies
// of env: blocks without needing a YAML round-trip (which would reorder keys
// and strip the comments the advisor benefits from reading).
var envValueLine = regexp.MustCompile(`^(\s+)([A-Za-z_][A-Za-z0-9_]*)(\s*:\s*)(.+)$`)

// RedactConfig blanks secret-bearing values in raw config text, preserving
// structure, ordering and comments so the advisor still sees the shape of the
// file it may be asked to patch.
func RedactConfig(raw string) string {
	lines := strings.Split(raw, "\n")
	inEnvBlock := false
	envIndent := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track env:/mcps env blocks, whose values are all potentially secret.
		if trimmed == "env:" || strings.HasSuffix(trimmed, " env:") {
			inEnvBlock = true
			envIndent = indentOf(line)
			continue
		}
		if inEnvBlock {
			if trimmed == "" {
				continue
			}
			if indentOf(line) <= envIndent {
				inEnvBlock = false
			} else if m := envValueLine.FindStringSubmatch(line); m != nil {
				lines[i] = m[1] + m[2] + m[3] + redactedFor(m[4])
				continue
			}
		}

		if m := secretKeys.FindStringSubmatch(line); m != nil {
			lines[i] = m[1] + m[2] + m[3] + redactedFor(m[4])
		}
	}
	return strings.Join(lines, "\n")
}

// redactedFor keeps a pure ${VAR} reference visible — it names an environment
// variable rather than carrying a secret, and the advisor may legitimately want
// to reason about which variable an agent uses.
func redactedFor(value string) string {
	v := strings.TrimSpace(value)
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") && !strings.Contains(v, " ") {
		return v
	}
	return "<redacted>"
}

func indentOf(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t"))
}

func dedupe(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := ss[:1]
	for _, s := range ss[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// Filter returns the workspace files relevant at a given breadth, given the set
// of agents that actually ran.
func (w *Workspace) Filter(breadth Breadth, active map[string]bool, flagged map[string]bool) []WorkspaceFile {
	if breadth == BreadthAll {
		return w.Files
	}
	want := active
	if breadth == BreadthFlagged {
		want = flagged
	}
	var out []WorkspaceFile
	for _, f := range w.Files {
		switch f.Kind {
		case KindConfig, KindWorkflow:
			out = append(out, f) // always in scope: it is what gets patched
		default:
			if f.Owner == "" || want[f.Owner] {
				out = append(out, f)
			}
		}
	}
	return out
}
