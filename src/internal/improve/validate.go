package improve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
)

// Stage names the validation step a proposal reached.
type Stage string

const (
	StagePath      Stage = "path"      // inside the workspace, not excluded
	StageApply     Stage = "apply"     // the diff applies cleanly to current content
	StageConfig    Stage = "config"    // the result parses and passes cfg.Validate()
	StageExpr      Stage = "expr"      // conditions still lint
	StageWarnings  Stage = "warnings"  // WorkflowWarnings on the candidate
	StageCritic    Stage = "critic"    // adversarial second opinion (deep effort)
	StageValidated Stage = "validated" // cleared everything applicable
)

// Verdict is the outcome of validating one recommendation.
type Verdict struct {
	Recommendation Recommendation
	// Reached is the last stage entered. On failure it names where it stopped.
	Reached Stage
	OK      bool
	Reason  string

	// Path is the resolved absolute target, set once the path check passes.
	Path string
	// Result is the patched content, set once the patch applies.
	Result string
	// Added and Removed are the line counts, for the summary.
	Added, Removed int
	// NewWarnings are config warnings the patch introduces that were not already
	// present. Not a failure — shown alongside the diff.
	NewWarnings []string
	// MachineChecked reports whether anything beyond "it applies" could be
	// verified. False for prose targets (souls, skills), where nothing can be.
	MachineChecked bool
	// Critique is the adversarial second opinion, when the critic pass ran.
	Critique *Critique
}

// Validator applies the five-stage gate from the proposal.
type Validator struct {
	Workspace  *Workspace
	ConfigPath string
	// BaselineWarnings are the warnings the current config already emits, so only
	// warnings a patch *introduces* are reported.
	BaselineWarnings map[string]bool
}

// NewValidator prepares a validator, recording the config's existing warnings so
// pre-existing noise is not blamed on a proposal.
func NewValidator(ws *Workspace, configPath string, cfg *config.Config) *Validator {
	base := map[string]bool{}
	if cfg != nil {
		for _, w := range cfg.WorkflowWarnings() {
			base[w] = true
		}
	}
	return &Validator{Workspace: ws, ConfigPath: configPath, BaselineWarnings: base}
}

// Validate runs every recommendation through the gate, in order, short-circuiting
// on the first failure and recording where it stopped.
func (v *Validator) Validate(recs []Recommendation) []Verdict {
	out := make([]Verdict, 0, len(recs))
	for _, r := range recs {
		out = append(out, v.validateOne(r))
	}
	return out
}

func (v *Validator) validateOne(r Recommendation) Verdict {
	verdict := Verdict{Recommendation: r, Reached: StagePath}

	// A recommendation with no patch is not a failure: proposing a change that
	// needs human judgement is legitimate and often the honest answer.
	if strings.TrimSpace(r.Patch) == "" {
		verdict.OK = true
		verdict.Reached = StageValidated
		verdict.Reason = "no patch — advisory only"
		return verdict
	}

	patch, err := ParsePatch(r.Patch)
	if err != nil {
		verdict.Reason = err.Error()
		return verdict
	}

	// ── 1. path ──────────────────────────────────────────────────────────────
	target := patch.Path
	if r.File != "" && r.File != target {
		// The stated file and the diff header disagree. Trust neither.
		verdict.Reason = fmt.Sprintf("recommendation names %q but the diff targets %q", r.File, target)
		return verdict
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(v.Workspace.Root, target)
	}
	abs = filepath.Clean(abs)
	if Excluded(abs, v.Workspace.Root) {
		verdict.Reason = fmt.Sprintf("%s is outside the config workspace or on the exclusion list", target)
		return verdict
	}
	if !v.inWorkspace(abs) {
		verdict.Reason = fmt.Sprintf("%s is not a file the advisor was shown", target)
		return verdict
	}
	verdict.Path = abs

	// ── 2. apply ─────────────────────────────────────────────────────────────
	verdict.Reached = StageApply
	original, err := os.ReadFile(abs)
	if err != nil {
		verdict.Reason = fmt.Sprintf("cannot read %s: %v", target, err)
		return verdict
	}
	patched, err := patch.Apply(string(original))
	if err != nil {
		verdict.Reason = err.Error()
		return verdict
	}
	verdict.Result = patched
	verdict.Added, verdict.Removed = patch.Stats()

	// Prose targets stop here. There is nothing to lint in a markdown
	// instruction file, so a bad soul or skill edit cannot be caught
	// mechanically — only by review, or by its effect on later runs. Saying so
	// is the point: a reviewer must know which hunks were checked and which
	// merely applied.
	if !v.isConfigLike(abs) {
		verdict.OK = true
		verdict.Reached = StageValidated
		verdict.MachineChecked = false
		return verdict
	}
	verdict.MachineChecked = true

	// ── 3. config ────────────────────────────────────────────────────────────
	verdict.Reached = StageConfig
	candidate, err := v.loadCandidate(abs, patched)
	if err != nil {
		verdict.Reason = err.Error()
		return verdict
	}
	if errs := candidate.Validate(); len(errs) > 0 {
		verdict.Reason = fmt.Sprintf("the patched config does not validate: %v", errs[0])
		return verdict
	}

	// ── 4. expression lint ───────────────────────────────────────────────────
	// cfg.Validate already runs LintExpr over conditions when the hook is
	// installed (cli.init wires it). Naming the stage separately keeps the
	// failure legible: an invalid `if:` reads as an expression problem rather
	// than a generic config error.
	verdict.Reached = StageExpr

	// ── 5. warnings ──────────────────────────────────────────────────────────
	verdict.Reached = StageWarnings
	for _, w := range candidate.WorkflowWarnings() {
		if !v.BaselineWarnings[w] {
			verdict.NewWarnings = append(verdict.NewWarnings, w)
		}
	}

	verdict.OK = true
	verdict.Reached = StageValidated
	return verdict
}

// loadCandidate copies the config directory to a temp dir, substitutes the
// patched file, and loads from there — so `uses:` references and soul_file paths
// resolve exactly as they would in place.
//
// The same mirroring covers a patch to the main config and to a referenced
// workflow file beside it: both are files config.Load pulls in, and neither can
// be validated by re-reading the original path.
//
// Loading from a temp directory rather than mutating the real file matters: a
// proposal must never touch disk before the operator has seen it.
func (v *Validator) loadCandidate(target, patched string) (*config.Config, error) {
	dir, err := os.MkdirTemp("", "apiary-improve-*")
	if err != nil {
		return nil, fmt.Errorf("creating validation sandbox: %w", err)
	}
	defer os.RemoveAll(dir)

	cfgAbs := v.absConfigPath()
	srcRoot := filepath.Dir(cfgAbs)

	// Mirror the YAML tree, not just the top level: a reusable workflow can be
	// referenced by `uses:` from a subdirectory, and it has to be patchable too.
	// Only YAML is copied — the config directory also holds the database, logs
	// and transcripts, none of which config.Load reads.
	substituted := false
	err = filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable corner of the tree; config.Load would skip it too
		}
		if d.IsDir() {
			if Excluded(path, srcRoot) {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(path)); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		rel, relErr := filepath.Rel(srcRoot, path)
		if relErr != nil {
			return nil
		}
		dest := filepath.Join(dir, rel)
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o700); mkErr != nil {
			return mkErr
		}

		body := []byte(patched)
		if filepath.Clean(path) == filepath.Clean(target) {
			substituted = true
		} else {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}
			body = data
		}
		return os.WriteFile(dest, body, 0o600)
	})
	if err != nil {
		return nil, fmt.Errorf("mirroring config for validation: %w", err)
	}

	// If the patched file never made it into the mirror, validation would run
	// against the ORIGINAL content and pass — reporting a proposal as verified
	// when nothing about it was checked. Refusing is the only safe outcome.
	if !substituted {
		return nil, fmt.Errorf("cannot validate a change to %s: it is not part of the config tree under %s",
			target, srcRoot)
	}

	mirrored := filepath.Join(dir, filepath.Base(cfgAbs))
	cfg, err := config.Load(mirrored)
	if err != nil {
		return nil, fmt.Errorf("the patched config does not parse: %w", err)
	}
	return cfg, nil
}

func (v *Validator) absConfigPath() string {
	abs, err := filepath.Abs(v.ConfigPath)
	if err != nil {
		return v.ConfigPath
	}
	return abs
}

// isConfigLike reports whether a target is YAML the config loader understands,
// as opposed to a prose instruction file.
func (v *Validator) isConfigLike(abs string) bool {
	ext := strings.ToLower(filepath.Ext(abs))
	return ext == ".yaml" || ext == ".yml"
}

// inWorkspace reports whether the target is a file discovery actually surfaced.
// Patching a file the advisor was never shown means it is working from
// assumption rather than evidence.
func (v *Validator) inWorkspace(abs string) bool {
	for _, f := range v.Workspace.Files {
		candidate := f.Path
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(v.Workspace.Root, candidate)
		}
		if filepath.Clean(candidate) == abs {
			return true
		}
	}
	return false
}

// Summarize splits verdicts into the ones worth showing as changes and the ones
// that could not be validated.
func Summarize(verdicts []Verdict) (accepted, rejected []Verdict) {
	for _, v := range verdicts {
		if v.OK {
			accepted = append(accepted, v)
		} else {
			rejected = append(rejected, v)
		}
	}
	return accepted, rejected
}
