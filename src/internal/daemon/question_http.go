package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

const (
	questionDefaultTimeout = 30 * time.Minute
	questionMaxTimeout     = 24 * time.Hour
	// questionPollInterval is how often a long-poll re-reads its row. There is no
	// notification channel between the answer endpoint and the waiting agent, and
	// adding one (pub/sub, a condition variable keyed by id, a dedicated broker)
	// would buy a few hundred milliseconds of latency for a human-driven flow at
	// the cost of a whole lifecycle to keep correct. Re-reading one indexed row is
	// simple and cannot leak.
	questionPollInterval = 250 * time.Millisecond
	questionDefaultWait  = 20
	questionMaxWait      = 30
)

// handleQuestions is GET /questions (dashboard list) and POST /questions (agent
// asks). The MCP server is the only POST caller in practice; the list is what a
// dashboard polls to show who is blocked on what.
func (d *Dispatcher) handleQuestions(w http.ResponseWriter, r *http.Request) {
	if d.db == nil {
		http.Error(w, "question store unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		questions, err := d.db.ListQuestions(r.Context(), r.URL.Query().Get("status"), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if questions == nil {
			questions = []db.AgentQuestion{}
		}
		writeJSON(w, http.StatusOK, questions)
	case http.MethodPost:
		d.createQuestion(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type createQuestionRequest struct {
	WorkflowInstanceID string              `json:"workflow_instance_id"`
	StepID             string              `json:"step_id"`
	TaskID             string              `json:"task_id"`
	AgentID            string              `json:"agent_id"`
	Question           string              `json:"question"`
	Options            []db.QuestionOption `json:"options"`
	// AllowFreeText is a pointer so an absent field means the default (true),
	// not the Go zero value. A tool that offers options still usually wants to
	// accept a typed answer.
	AllowFreeText  *bool `json:"allow_free_text"`
	TimeoutSeconds int   `json:"timeout_seconds"`
}

func (d *Dispatcher) createQuestion(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var req createQuestionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}
	allowFreeText := true
	if req.AllowFreeText != nil {
		allowFreeText = *req.AllowFreeText
	}
	timeout := questionDefaultTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	if timeout > questionMaxTimeout {
		timeout = questionMaxTimeout
	}
	now := time.Now().UTC()
	expires := now.Add(timeout)
	q := &db.AgentQuestion{
		WorkflowInstanceID: req.WorkflowInstanceID,
		StepID:             req.StepID,
		TaskID:             req.TaskID,
		AgentID:            req.AgentID,
		Question:           req.Question,
		Options:            req.Options,
		AllowFreeText:      allowFreeText,
		CreatedAt:          now,
		ExpiresAt:          &expires,
	}
	if err := d.db.CreateQuestion(r.Context(), q); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": q.ID})
}

// handleQuestion routes /questions/{id} (GET, long-poll) and
// /questions/{id}/answer (POST).
func (d *Dispatcher) handleQuestion(w http.ResponseWriter, r *http.Request) {
	if d.db == nil {
		http.Error(w, "question store unavailable", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/questions/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		d.getQuestion(w, r, id)
	case len(parts) == 2 && parts[1] == "answer" && r.Method == http.MethodPost:
		d.answerQuestion(w, r, id)
	case len(parts) == 1:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

type questionView struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Answer     string `json:"answer,omitempty"`
	Reason     string `json:"reason,omitempty"`
	AnsweredBy string `json:"answered_by,omitempty"`
}

// getQuestion is the long-poll the MCP server loops on. It waits up to `wait`
// seconds for an answer, then returns whatever state the row is in — pending is
// a valid response meaning "ask again", not an error. Time spent waiting is
// bounded by the client's own deadline as well, so a dead daemon cannot strand
// the agent; the MCP side always gives up on its own clock.
func (d *Dispatcher) getQuestion(w http.ResponseWriter, r *http.Request, id string) {
	wait := questionDefaultWait
	if v := r.URL.Query().Get("wait"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			wait = n
		}
	}
	if wait <= 0 {
		wait = questionDefaultWait
	}
	if wait > questionMaxWait {
		wait = questionMaxWait
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		q, err := d.db.GetQuestion(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if q == nil {
			http.Error(w, "question not found", http.StatusNotFound)
			return
		}
		// Check the deadline locally and only write when it has actually passed.
		// Sweeping on every tick would turn each waiting agent into a stream of
		// UPDATEs; this way the hot path is a single indexed read.
		if q.Status == db.QuestionPending && q.ExpiresAt != nil && !time.Now().Before(*q.ExpiresAt) {
			_, _ = d.db.ExpireQuestions(r.Context())
			if q, err = d.db.GetQuestion(r.Context(), id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if q.Status != db.QuestionPending || !time.Now().Before(deadline) {
			writeJSON(w, http.StatusOK, questionView{
				ID: q.ID, Status: q.Status, Answer: q.Answer,
				Reason: q.AnswerReason, AnsweredBy: q.AnsweredBy,
			})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(questionPollInterval):
		}
	}
}

type answerQuestionRequest struct {
	Answer string `json:"answer"`
	Reason string `json:"reason"`
	Actor  string `json:"actor"`
}

func (d *Dispatcher) answerQuestion(w http.ResponseWriter, r *http.Request, id string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var req answerQuestionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	_, _ = d.db.ExpireQuestions(r.Context())
	q, err := d.db.GetQuestion(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if q == nil {
		http.Error(w, "question not found", http.StatusNotFound)
		return
	}
	// An already answered or expired question is a conflict, not a second answer:
	// the agent has already moved on with the first answer, or with none.
	if q.Status != db.QuestionPending {
		http.Error(w, fmt.Sprintf("question %s is already %s", q.ID, q.Status), http.StatusConflict)
		return
	}
	answer, err := validateQuestionAnswer(q, req.Answer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, won, err := d.db.AnswerQuestion(r.Context(), q.ID, answer, req.Reason, req.Actor)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !won {
		// Another answer (dashboard vs long-poll race, or the expiry sweep above)
		// won first. Report the same conflict the pre-check would have.
		http.Error(w, "question is no longer pending", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}

// validateQuestionAnswer applies the question's declared options the same way
// approvals match outcomes: case-insensitive and trimmed, because the value can
// arrive from a JSON client, a CLI flag or a text box. Free text is allowed only
// when the question says so; an empty answer is a legitimate "I have nothing to
// add" reply in that mode.
func validateQuestionAnswer(q *db.AgentQuestion, answer string) (string, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		if !q.AllowFreeText {
			return "", fmt.Errorf("an answer is required: the question does not allow free text")
		}
		return "", nil
	}
	if len(q.Options) == 0 {
		if !q.AllowFreeText {
			return "", fmt.Errorf("the question declares no options and does not allow free text")
		}
		return answer, nil
	}
	for _, o := range q.Options {
		if strings.EqualFold(strings.TrimSpace(o.Value), answer) {
			// Store the declared value, not the caller's casing, so a consumer
			// matching options sees the canonical string.
			return o.Value, nil
		}
	}
	if !q.AllowFreeText {
		return "", fmt.Errorf("answer must be one of the question's options")
	}
	return answer, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
