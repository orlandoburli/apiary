package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/db"
	"github.com/orlandoburli/apiary/internal/model"
)

func newQuestionTestDispatcher(t *testing.T) (*Dispatcher, *db.Client) {
	t.Helper()
	ctx := context.Background()
	dbc, err := db.New(ctx, filepath.Join(t.TempDir(), "questions.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	return &Dispatcher{db: dbc, cfg: &config.Config{}}, dbc
}

func postJSON(t *testing.T, d *Dispatcher, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	w := httptest.NewRecorder()
	d.handleQuestion(w, req)
	return w
}

func TestCreateQuestionRequiresText(t *testing.T) {
	d, _ := newQuestionTestDispatcher(t)
	body := `{"workflow_instance_id":"wf","step_id":"s","question":"   "}`
	req := httptest.NewRequest(http.MethodPost, "/questions", bytes.NewReader([]byte(body)))
	w := httptest.NewRecorder()
	d.handleQuestions(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateQuestionDefaultsFreeTextAndTimeout(t *testing.T) {
	d, dbc := newQuestionTestDispatcher(t)
	ctx := context.Background()
	body := `{"workflow_instance_id":"wf","step_id":"s","task_id":"t","agent_id":"a","question":"Proceed?","options":[{"value":"yes","label":"Yes"}],"timeout_seconds":60}`
	req := httptest.NewRequest(http.MethodPost, "/questions", bytes.NewReader([]byte(body)))
	w := httptest.NewRecorder()
	d.handleQuestions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.ID == "" {
		t.Fatalf("body=%s err=%v", w.Body.String(), err)
	}
	q, err := dbc.GetQuestion(ctx, out.ID)
	if err != nil || q == nil {
		t.Fatalf("get: %v %v", q, err)
	}
	if !q.AllowFreeText {
		t.Fatal("allow_free_text should default to true")
	}
	if q.ExpiresAt == nil || time.Until(*q.ExpiresAt) > 61*time.Second {
		t.Fatalf("expires_at=%v, want ~60s", q.ExpiresAt)
	}

	// The dashboard polls this list.
	list := httptest.NewRecorder()
	d.handleQuestions(list, httptest.NewRequest(http.MethodGet, "/questions?status=pending&limit=5", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d", list.Code)
	}
	var questions []db.AgentQuestion
	if err := json.Unmarshal(list.Body.Bytes(), &questions); err != nil {
		t.Fatal(err)
	}
	if len(questions) != 1 || questions[0].ID != out.ID {
		t.Fatalf("questions=%+v", questions)
	}
}

func TestQuestionAnswerValidation(t *testing.T) {
	d, dbc := newQuestionTestDispatcher(t)
	ctx := context.Background()
	q := &db.AgentQuestion{
		WorkflowInstanceID: "wf",
		Question:           "Pick a branch",
		Options:            []db.QuestionOption{{Value: "main"}, {Value: "release"}},
		AllowFreeText:      false,
	}
	if err := dbc.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}

	if w := postJSON(t, d, "/questions/"+q.ID+"/answer", `{"answer":"","actor":"alice"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty answer status=%d body=%s", w.Code, w.Body.String())
	}
	if w := postJSON(t, d, "/questions/"+q.ID+"/answer", `{"answer":"maybe","actor":"alice"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown option status=%d body=%s", w.Code, w.Body.String())
	}
	// Matching is trimmed and case-insensitive, and the stored value is the
	// declared option rather than the caller's casing.
	if w := postJSON(t, d, "/questions/"+q.ID+"/answer", `{"answer":"  MAIN ","reason":"ok","actor":"alice"}`); w.Code != http.StatusOK {
		t.Fatalf("valid option status=%d body=%s", w.Code, w.Body.String())
	}
	stored, _ := dbc.GetQuestion(ctx, q.ID)
	if stored.Status != db.QuestionAnswered || stored.Answer != "main" || stored.AnsweredBy != "alice" || stored.AnswerReason != "ok" {
		t.Fatalf("stored=%+v", stored)
	}
	if w := postJSON(t, d, "/questions/"+q.ID+"/answer", `{"answer":"release","actor":"bob"}`); w.Code != http.StatusConflict {
		t.Fatalf("second answer status=%d body=%s, want 409", w.Code, w.Body.String())
	}
}

func TestQuestionAnswerFreeText(t *testing.T) {
	d, dbc := newQuestionTestDispatcher(t)
	ctx := context.Background()
	q := &db.AgentQuestion{Question: "Anything else?", AllowFreeText: true}
	if err := dbc.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if w := postJSON(t, d, "/questions/"+q.ID+"/answer", `{"answer":"use the staging key","actor":"alice"}`); w.Code != http.StatusOK {
		t.Fatalf("free text status=%d body=%s", w.Code, w.Body.String())
	}
	stored, _ := dbc.GetQuestion(ctx, q.ID)
	if stored.Answer != "use the staging key" {
		t.Fatalf("answer=%q", stored.Answer)
	}
}

func TestQuestionLongPollReturnsAnswer(t *testing.T) {
	d, dbc := newQuestionTestDispatcher(t)
	ctx := context.Background()
	q := &db.AgentQuestion{Question: "Go?", AllowFreeText: true}
	if err := dbc.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dbc.AnswerQuestion(ctx, q.ID, "yes", "because", "alice"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/questions/"+q.ID+"?wait=5", nil)
	w := httptest.NewRecorder()
	d.handleQuestion(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Status     string `json:"status"`
		Answer     string `json:"answer"`
		Reason     string `json:"reason"`
		AnsweredBy string `json:"answered_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != db.QuestionAnswered || out.Answer != "yes" || out.Reason != "because" || out.AnsweredBy != "alice" {
		t.Fatalf("out=%+v", out)
	}
}

func TestQuestionLongPollExpiresInsteadOfHanging(t *testing.T) {
	d, dbc := newQuestionTestDispatcher(t)
	ctx := context.Background()
	past := time.Now().UTC().Add(-time.Second)
	q := &db.AgentQuestion{Question: "Still there?", AllowFreeText: true, ExpiresAt: &past}
	if err := dbc.CreateQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	req := httptest.NewRequest(http.MethodGet, "/questions/"+q.ID+"?wait=10", nil)
	w := httptest.NewRecorder()
	d.handleQuestion(w, req)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("expired question held the poll for %v", elapsed)
	}
	var out struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Status != db.QuestionTimedOut {
		t.Fatalf("status=%q body=%s", out.Status, w.Body.String())
	}
	if w := postJSON(t, d, "/questions/"+q.ID+"/answer", `{"answer":"late","actor":"alice"}`); w.Code != http.StatusConflict {
		t.Fatalf("answering expired question status=%d body=%s, want 409", w.Code, w.Body.String())
	}
}

func TestQuestionLongPollUnknownIs404(t *testing.T) {
	d, _ := newQuestionTestDispatcher(t)
	req := httptest.NewRequest(http.MethodGet, "/questions/nope?wait=1", nil)
	w := httptest.NewRecorder()
	d.handleQuestion(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestQuestionMCPServerInjection(t *testing.T) {
	d := &Dispatcher{cfg: &config.Config{}, configFile: filepath.Join(t.TempDir(), "apiary.yaml")}
	cfgMap := d.runnerConfigWithMCPs(map[string]any{"command": "claude"}, nil, nil)
	mcps, ok := cfgMap["mcps"].([]model.MCPServer)
	if !ok || len(mcps) != 1 {
		t.Fatalf("mcps=%v", cfgMap["mcps"])
	}
	srv := mcps[0]
	if srv.Name != "apiary" || len(srv.Args) != 1 || srv.Args[0] != "mcp-serve" || srv.Command == "" || srv.Env["APIARY_SOCKET"] == "" {
		t.Fatalf("server=%+v", srv)
	}

	// A user-declared apiary server wins; the built-in one is not appended twice.
	declared := []model.MCPServer{{Name: "apiary", Command: "custom-binary"}}
	cfgMap = d.runnerConfigWithMCPs(map[string]any{}, declared, nil)
	mcps = cfgMap["mcps"].([]model.MCPServer)
	if len(mcps) != 1 || mcps[0].Command != "custom-binary" {
		t.Fatalf("declared mcps=%+v", mcps)
	}

	// settings.questions.enabled: false removes the injection entirely.
	disabled := false
	d.cfg.Settings.Questions.Enabled = &disabled
	if got := d.runnerConfigWithMCPs(map[string]any{"command": "claude"}, nil, nil); got["mcps"] != nil {
		t.Fatalf("disabled still injected mcps=%v", got["mcps"])
	}
}
