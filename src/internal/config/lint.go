package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// removedDirective describes a config key that older versions accepted but the
// current engine no longer honors. Detecting these is what turns a silent no-op
// (the directive is parsed but ignored) into a clear, actionable error.
type removedDirective struct {
	// path is the dotted YAML key path, e.g. "on_complete.assign_from_output".
	// Only the last two segments are matched: the leaf key and (when present) its
	// immediate parent key, which is enough to disambiguate without a full schema.
	path string
	// message explains what was removed and how to achieve the same result now.
	message string
}

// removedDirectives is the registry of directives that were removed but still
// parse into the structs (so a plain YAML decode accepts them silently). Add an
// entry here whenever a directive is dropped so users get told instead of
// debugging a pipeline that quietly does nothing.
var removedDirectives = []removedDirective{
	{
		path: "routes",
		message: "`routes` was removed: task routing is now done exclusively through workflow " +
			"triggers (`workflows[].trigger`). Replace each route with a workflow that has a " +
			"`trigger:` block matching the same criteria.\n" +
			"  See .apiary/example-workflow.yaml for the trigger pattern.",
	},
	{
		path: "depends_on",
		message: "`depends_on` was removed from step config: the v2 workflow engine uses " +
			"implicit sequential ordering (steps run in the order they are declared). " +
			"Remove the `depends_on` field — ordering is automatic.",
	},
	{
		path: "on_complete.assign_from_output",
		message: "`on_complete.assign_from_output` was removed: the workflow engine no longer " +
			"relabels-and-repolls to hand a task off to another agent, so this directive does " +
			"nothing — the classifier runs and the task is never routed onward.\n" +
			"  Fix: route new work with a `triage` workflow that classifies and routes in ONE " +
			"instance using a `split` step (classify → split → agent).\n" +
			"  See .apiary/example-workflow.yaml for the pattern.",
	},
	{
		path: "on_complete.assign_label_prefix",
		message: "`on_complete.assign_label_prefix` was removed: it only configured the removed " +
			"`assign_from_output` directive and now does nothing.\n" +
			"  Fix: drop it and route via a `triage` workflow with a `split` step " +
			"(see .apiary/example-workflow.yaml).",
	},
}

// lint runs the post-parse config checks that a plain YAML decode cannot express:
// removed directives that still parse silently, and unknown/typo'd keys. It
// operates on the raw config text captured at load time, so it is a no-op for
// configs constructed directly in code or via LoadDefaults (no rawContent).
func (c *Config) lint() []error {
	if c.rawContent == "" {
		return nil
	}
	var errs []error
	errs = append(errs, c.lintRemovedDirectives()...)
	errs = append(errs, c.lintUnknownFields()...)
	return errs
}

// lintRemovedDirectives walks the raw YAML and reports any removed directive from
// the registry, with the source line so the user can jump straight to it.
func (c *Config) lintRemovedDirectives() []error {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(c.rawContent), &root); err != nil {
		// A YAML syntax error is reported by the real decode in Load; skip here.
		return nil
	}

	var errs []error
	walkYAMLKeys(&root, "", func(key, parentKey string, line int) {
		for _, d := range removedDirectives {
			leaf, parent := splitLeaf(d.path)
			if key != leaf {
				continue
			}
			if parent != "" && parentKey != parent {
				continue
			}
			errs = append(errs, fmt.Errorf("line %d: %s", line, d.message))
		}
	})
	return errs
}

// lintUnknownFields strict-decodes the raw config and surfaces any key that has no
// matching struct field — typically a typo (e.g. `lables:`) or a stray directive.
// Free-form blocks (source/runner `config:` are map[string]any) accept any key, so
// they never trip this. Removed directives that still exist as struct fields are
// "known" to the decoder and are reported by lintRemovedDirectives instead, so
// there is no double-reporting.
func (c *Config) lintUnknownFields() []error {
	dec := yaml.NewDecoder(strings.NewReader(expandEnv(c.rawContent)))
	dec.KnownFields(true)

	var probe Config
	err := dec.Decode(&probe)
	if err == nil {
		return nil
	}

	var typeErr *yaml.TypeError
	if !asTypeError(err, &typeErr) {
		// Non-strictness parse errors are surfaced by the real decode in Load.
		return nil
	}

	var errs []error
	for _, msg := range typeErr.Errors {
		errs = append(errs, fmt.Errorf("unknown config field — %s (typo, or a directive that does not exist)", msg))
	}
	return errs
}

// walkYAMLKeys visits every mapping key in the tree, passing the key name, its
// immediate parent key (""for the root), and the key's source line.
func walkYAMLKeys(n *yaml.Node, parentKey string, visit func(key, parentKey string, line int)) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			walkYAMLKeys(c, parentKey, visit)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			visit(k.Value, parentKey, k.Line)
			walkYAMLKeys(v, k.Value, visit)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			walkYAMLKeys(c, parentKey, visit)
		}
	}
}

// splitLeaf returns the last path segment (the leaf key) and the segment before it
// (its expected parent key, or "" when the path has a single segment).
func splitLeaf(path string) (leaf, parent string) {
	parts := strings.Split(path, ".")
	leaf = parts[len(parts)-1]
	if len(parts) >= 2 {
		parent = parts[len(parts)-2]
	}
	return leaf, parent
}

// asTypeError reports whether err is a *yaml.TypeError, assigning it to target.
// (errors.As avoids importing the errors package elsewhere in this file.)
func asTypeError(err error, target **yaml.TypeError) bool {
	te, ok := err.(*yaml.TypeError)
	if ok {
		*target = te
	}
	return ok
}
