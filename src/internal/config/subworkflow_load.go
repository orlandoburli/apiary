package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// resolveLocalWorkflows expands every local uses reference into the existing
// workflow registry. The primary Config remains the owner of runners, agents,
// sources, and settings; reusable files contain exactly one WorkflowConfig.
func resolveLocalWorkflows(configPath string, cfg *Config) error {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolving config path: %w", err)
	}
	r := &workflowFileResolver{
		cfg:      cfg,
		byFile:   map[string]string{},
		byID:     map[string]string{},
		visiting: map[string]int{},
	}
	for _, wf := range cfg.Workflows {
		if wf.ID != "" {
			r.byID[wf.ID] = abs
		}
	}
	for i := range cfg.Workflows {
		if err := r.resolveSteps(&cfg.Workflows[i], abs); err != nil {
			return err
		}
	}
	return nil
}

type workflowFileResolver struct {
	cfg      *Config
	byFile   map[string]string // canonical file -> workflow id
	byID     map[string]string // workflow id -> declaring file
	visiting map[string]int    // canonical file -> stack index
	stack    []string
}

func (r *workflowFileResolver) resolveSteps(wf *WorkflowConfig, declaringFile string) error {
	for i := range wf.Steps {
		if err := r.resolveStep(wf.ID, &wf.Steps[i], declaringFile); err != nil {
			return err
		}
	}
	return nil
}

func (r *workflowFileResolver) resolveStep(workflowID string, s *StepConfig, declaringFile string) error {
	if s.Uses != "" {
		if s.Workflow != "" {
			return fmt.Errorf("workflow %q step %q: use only one of uses or workflow", workflowID, s.ID)
		}
		path, err := resolveWorkflowPath(filepath.Dir(declaringFile), s.Uses)
		if err != nil {
			return fmt.Errorf("workflow %q step %q: %w", workflowID, s.ID, err)
		}
		childID, err := r.load(path)
		if err != nil {
			return fmt.Errorf("workflow %q step %q: %w", workflowID, s.ID, err)
		}
		s.Type = StepTypeWorkflow
		s.Workflow = childID
	}
	for i := range s.SubSteps {
		if err := r.resolveStep(workflowID, &s.SubSteps[i], declaringFile); err != nil {
			return err
		}
	}
	for i := range s.ParallelSteps {
		if err := r.resolveStep(workflowID, &s.ParallelSteps[i], declaringFile); err != nil {
			return err
		}
	}
	if s.Step != nil {
		if err := r.resolveStep(workflowID, s.Step, declaringFile); err != nil {
			return err
		}
	}
	return nil
}

func (r *workflowFileResolver) load(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		canonical = filepath.Clean(path)
	}
	if id, ok := r.byFile[canonical]; ok {
		return id, nil
	}
	if at, ok := r.visiting[canonical]; ok {
		cycle := append(append([]string{}, r.stack[at:]...), canonical)
		for i := range cycle {
			cycle[i] = filepath.Base(cycle[i])
		}
		return "", fmt.Errorf("cyclic subworkflow reference: %s", strings.Join(cycle, " -> "))
	}

	data, err := os.ReadFile(canonical)
	if err != nil {
		return "", fmt.Errorf("reading subworkflow %q: %w", canonical, err)
	}
	dec := yaml.NewDecoder(strings.NewReader(expandEnv(string(data))))
	dec.KnownFields(true)
	var child WorkflowConfig
	if err := dec.Decode(&child); err != nil {
		return "", fmt.Errorf("parsing subworkflow %q: %w", canonical, err)
	}
	if child.ID == "" {
		return "", fmt.Errorf("subworkflow %q: id is required", canonical)
	}
	if previous, exists := r.byID[child.ID]; exists && previous != canonical {
		return "", fmt.Errorf("subworkflow %q declares duplicate workflow id %q (already declared by %q)", canonical, child.ID, previous)
	}

	r.visiting[canonical] = len(r.stack)
	r.stack = append(r.stack, canonical)
	if err := r.resolveSteps(&child, canonical); err != nil {
		return "", err
	}
	r.stack = r.stack[:len(r.stack)-1]
	delete(r.visiting, canonical)

	r.byFile[canonical] = child.ID
	r.byID[child.ID] = canonical
	r.cfg.Workflows = append(r.cfg.Workflows, child)
	return child.ID, nil
}

func resolveWorkflowPath(baseDir, ref string) (string, error) {
	if strings.Contains(ref, "://") {
		return "", fmt.Errorf("remote subworkflow reference %q is not supported; use a local .yaml or .yml file", ref)
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	candidates := []string{path}
	if filepath.Ext(path) == "" {
		candidates = append(candidates, path+".yaml", path+".yml")
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			abs, absErr := filepath.Abs(candidate)
			if absErr != nil {
				return "", absErr
			}
			return filepath.Clean(abs), nil
		}
	}
	return "", fmt.Errorf("subworkflow file %q not found (resolved from %q)", ref, baseDir)
}
