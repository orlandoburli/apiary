package editor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/orlandoburli/apiary/internal/config"
	"gopkg.in/yaml.v3"
)

// handleGetConfig serves GET /api/config — the full EditorConfig for the
// current apiary.yaml, including agents, sources, runners, and workflows.
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ec := configToEditor(s.cfg, s.configPath)
	jsonOK(w, ec)
}

// RenderRequest is the body for POST /api/render.
type RenderRequest struct {
	Workflows []EditorWorkflow `json:"workflows"`
}

// RenderResponse is returned by POST /api/render.
type RenderResponse struct {
	YAML string `json:"yaml"`
}

// handleRender converts the editor's in-memory JSON model back to YAML so the
// browser can display it as a live preview.
func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req RenderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build a shallow copy of the loaded config, replacing only the workflows.
	merged := *s.cfg
	merged.Workflows = nil
	for _, ew := range req.Workflows {
		merged.Workflows = append(merged.Workflows, editorToWorkflow(ew))
	}
	out, err := yaml.Marshal(&merged)
	if err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, RenderResponse{YAML: string(out)})
}

// ValidateRequest is the body for POST /api/validate.
type ValidateRequest struct {
	Workflows []EditorWorkflow `json:"workflows"`
}

// ValidateResponse is returned by POST /api/validate.
type ValidateResponse struct {
	Errors   []ValidationError `json:"errors"`
	Warnings []string          `json:"warnings"`
	Valid    bool              `json:"valid"`
}

// handleValidate runs the full config.Validate() suite on the proposed
// workflows (merged with the loaded non-workflow config) and returns per-step
// annotated errors.
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	merged := *s.cfg
	merged.Workflows = nil
	for _, ew := range req.Workflows {
		merged.Workflows = append(merged.Workflows, editorToWorkflow(ew))
	}

	rawErrs := merged.Validate()
	warnings := merged.WorkflowWarnings()

	resp := ValidateResponse{Valid: len(rawErrs) == 0}
	for _, e := range rawErrs {
		ve := ValidationError{Message: e.Error()}
		ve.WorkflowID, ve.StepID = parseErrorPath(e.Error())
		resp.Errors = append(resp.Errors, ve)
	}
	resp.Warnings = warnings
	jsonOK(w, resp)
}

// DiffRequest is the body for POST /api/diff.
type DiffRequest struct {
	Workflows []EditorWorkflow `json:"workflows"`
}

// DiffResponse is returned by POST /api/diff.
type DiffResponse struct {
	Diff     string `json:"diff"`
	Original string `json:"original"`
	Proposed string `json:"proposed"`
	Changed  bool   `json:"changed"`
}

// handleDiff computes a unified diff between the on-disk YAML and the proposed
// changes, without writing anything.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req DiffRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	proposed, err := s.renderYAML(req.Workflows)
	if err != nil {
		http.Error(w, "render error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	original := string(s.rawYAML)
	diff := unifiedDiff(original, proposed)
	jsonOK(w, DiffResponse{
		Diff:     diff,
		Original: original,
		Proposed: proposed,
		Changed:  diff != "(no semantic changes)",
	})
}

// SaveRequest is the body for POST /api/save.
type SaveRequest struct {
	Workflows []EditorWorkflow `json:"workflows"`
}

// SaveResponse is returned by POST /api/save.
type SaveResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// handleSave validates the proposed workflows, then writes the merged config
// back to the apiary.yaml file. It reloads the in-memory config and rawYAML on
// success so subsequent renders reflect the saved state.
func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req SaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate before writing.
	merged := *s.cfg
	merged.Workflows = nil
	for _, ew := range req.Workflows {
		merged.Workflows = append(merged.Workflows, editorToWorkflow(ew))
	}
	if errs := merged.Validate(); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		jsonOK(w, SaveResponse{Error: fmt.Sprintf("validation failed: %s", strings.Join(msgs, "; "))})
		return
	}

	proposed, err := s.renderYAML(req.Workflows)
	if err != nil {
		jsonOK(w, SaveResponse{Error: "render error: " + err.Error()})
		return
	}
	if err := os.WriteFile(s.configPath, []byte(proposed), 0o600); err != nil {
		jsonOK(w, SaveResponse{Error: "write error: " + err.Error()})
		return
	}

	// Reload to keep in-memory state consistent with the file.
	newCfg, err := config.Load(s.configPath)
	if err == nil {
		s.cfg = newCfg
		s.rawYAML = []byte(proposed)
	}

	jsonOK(w, SaveResponse{OK: true})
}

// renderYAML merges the proposed EditorWorkflows with the loaded config and
// marshals the result to YAML text.
func (s *Server) renderYAML(ews []EditorWorkflow) (string, error) {
	merged := *s.cfg
	merged.Workflows = nil
	for _, ew := range ews {
		merged.Workflows = append(merged.Workflows, editorToWorkflow(ew))
	}
	out, err := yaml.Marshal(&merged)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// jsonOK writes v as JSON with status 200.
func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// pathErrorRe extracts workflow id and step id from a validation error message.
var (
	wfRe   = regexp.MustCompile(`workflows\[\d+\] "([^"]+)"`)
	stepRe = regexp.MustCompile(`steps\[\d+\] "([^"]+)"`)
)

func parseErrorPath(msg string) (workflowID, stepID string) {
	if m := wfRe.FindStringSubmatch(msg); len(m) > 1 {
		workflowID = m[1]
	}
	if m := stepRe.FindStringSubmatch(msg); len(m) > 1 {
		stepID = m[1]
	}
	return
}
