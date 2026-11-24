package github

import (
	"context"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orlandoburli/apiary/internal/config"
)

// ciReposCfg is the config of the motivating setup (#8151): a source configured
// for the tracker's repository whose PRs are allowed to live in the engine's
// repository, which runs no CI on its sources.
func ciReposCfg() map[string]any {
	return map[string]any{
		"repo":    "orlandoburli/project-erp",
		"api_key": "ghp_secret",
		"ci_repos": []any{
			map[string]any{
				"repo":            "orlandoburli/apiary",
				"require_checks":  false,
				"no_checks_after": "10m",
			},
			map[string]any{"repo": "OrlandoBurli/Other"},
		},
	}
}

// Connect must populate the per-repo policies from config.ci_repos: without that
// the allow-list exists in the YAML and nowhere in the daemon.
func TestConnect_ParsesCIRepos(t *testing.T) {
	a := &Adapter{}
	if err := a.Connect(context.Background(), ciReposCfg()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if len(a.ciRepos) != 2 {
		t.Fatalf("ciRepos = %d entries, want 2", len(a.ciRepos))
	}

	first := a.ciRepos[0]
	if first.owner != "orlandoburli" || first.repo != "apiary" {
		t.Errorf("ciRepos[0] = %s/%s, want orlandoburli/apiary", first.owner, first.repo)
	}
	if first.requireChecks {
		t.Error("ciRepos[0].requireChecks = true, want the configured false")
	}
	if first.noChecksAfter != 10*time.Minute {
		t.Errorf("ciRepos[0].noChecksAfter = %v, want 10m", first.noChecksAfter)
	}

	// An entry that omits both knobs gets the documented defaults: checks are not
	// required, and the grace period is the default 10m.
	second := a.ciRepos[1]
	if second.requireChecks {
		t.Error("ciRepos[1].requireChecks = true, want the default false")
	}
	if second.noChecksAfter != defaultNoChecksAfter {
		t.Errorf("ciRepos[1].noChecksAfter = %v, want %v", second.noChecksAfter, defaultNoChecksAfter)
	}
}

// No ci_repos means no foreign repository is pollable — the pre-#8151 behaviour,
// unchanged.
func TestConnect_NoCIReposLeavesNoPolicies(t *testing.T) {
	a := &Adapter{}
	if err := a.Connect(context.Background(), map[string]any{"repo": "o/r"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if len(a.ciRepos) != 0 {
		t.Errorf("ciRepos = %v, want none", a.ciRepos)
	}
}

// A malformed entry is a configuration error: dropping it silently would leave
// the wait pending until max_duration for a repository the operator believed was
// allowed.
func TestConnect_RejectsMalformedCIRepo(t *testing.T) {
	for name, entry := range map[string]any{
		"no repo key":     map[string]any{"require_checks": false},
		"no slash":        map[string]any{"repo": "apiary"},
		"empty repo part": map[string]any{"repo": "orlandoburli/"},
		"not a mapping":   "orlandoburli/apiary",
	} {
		t.Run(name, func(t *testing.T) {
			a := &Adapter{}
			err := a.Connect(context.Background(), map[string]any{
				"repo": "orlandoburli/project-erp", "ci_repos": []any{entry},
			})
			if err == nil {
				t.Fatal("Connect accepted a malformed ci_repos entry")
			}
			if !strings.Contains(err.Error(), "ci_repos") {
				t.Errorf("error %q does not name ci_repos", err)
			}
		})
	}

	a := &Adapter{}
	if err := a.Connect(context.Background(), map[string]any{
		"repo": "orlandoburli/project-erp", "ci_repos": "orlandoburli/apiary",
	}); err == nil {
		t.Error("Connect accepted a non-list ci_repos")
	}
}

// The source's own repository is always require_checks: true, so an entry naming
// it can only be a mistake — accept it and the operator would believe they had
// relaxed the gate they cannot relax.
func TestConnect_RejectsOwnRepoInCIRepos(t *testing.T) {
	a := &Adapter{}
	err := a.Connect(context.Background(), map[string]any{
		"repo": "orlandoburli/project-erp",
		"ci_repos": []any{
			map[string]any{"repo": "OrlandoBurli/Project-ERP", "require_checks": false},
		},
	})
	if err == nil {
		t.Fatal("Connect accepted the source's own repository as a ci_repos entry")
	}
	if !strings.Contains(err.Error(), "own repository") {
		t.Errorf("error %q does not explain that it is the source's own repository", err)
	}
}

func TestConnect_RejectsBadNoChecksAfter(t *testing.T) {
	for name, value := range map[string]any{"unparseable": "soon", "not positive": "0s", "not a duration string": 600} {
		t.Run(name, func(t *testing.T) {
			a := &Adapter{}
			err := a.Connect(context.Background(), map[string]any{
				"repo":     "orlandoburli/project-erp",
				"ci_repos": []any{map[string]any{"repo": "orlandoburli/apiary", "no_checks_after": value}},
			})
			if err == nil {
				t.Fatalf("Connect accepted no_checks_after %v", value)
			}
			if !strings.Contains(err.Error(), "no_checks_after") {
				t.Errorf("error %q does not name no_checks_after", err)
			}
		})
	}
}

// A misspelled knob inside an entry must fail loudly: the entry would otherwise
// be accepted with the opposite policy to the one the operator wrote.
func TestConnect_RejectsUnknownCIRepoKey(t *testing.T) {
	a := &Adapter{}
	err := a.Connect(context.Background(), map[string]any{
		"repo": "orlandoburli/project-erp",
		"ci_repos": []any{
			map[string]any{"repo": "orlandoburli/apiary", "require_check": false},
		},
	})
	if err == nil {
		t.Fatal("Connect accepted a misspelled ci_repos key")
	}
	if !strings.Contains(err.Error(), "require_check") {
		t.Errorf("error %q does not name the offending key", err)
	}
}

// The policy has to survive the real decoding path, not just a hand-built map:
// yaml.v3 hands a nested list over as []any of map[string]any, and a mismatch
// there would make every documented ci_repos entry fail at daemon start.
func TestConnect_ReadsCIReposFromYAML(t *testing.T) {
	const doc = `version: "1"
sources:
  - id: gh
    type: github
    config:
      repo: orlandoburli/project-erp
      ci_repos:
        - repo: orlandoburli/apiary
          require_checks: false
          no_checks_after: 10m
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("decoding config: %v", err)
	}
	if len(cfg.Sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(cfg.Sources))
	}

	a := &Adapter{}
	if err := a.Connect(context.Background(), cfg.Sources[0].Config); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if len(a.ciRepos) != 1 {
		t.Fatalf("ciRepos = %v, want one entry", a.ciRepos)
	}
	got := a.ciRepos[0]
	if got.owner != "orlandoburli" || got.repo != "apiary" || got.requireChecks || got.noChecksAfter != 10*time.Minute {
		t.Errorf("policy = %+v, want orlandoburli/apiary require_checks=false no_checks_after=10m", got)
	}
}
