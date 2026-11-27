package dashboard

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
)

// startQuestionSocket stands in for the daemon's Unix socket and reports the
// JSON the dashboard posts to /questions/{id}/answer.
func startQuestionSocket(t *testing.T, status int, reply string) (string, <-chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("", "apiary-question")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "apiary.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	captured := make(chan map[string]any, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		captured <- payload
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = os.RemoveAll(dir)
	})
	return sock, captured
}

func sampleQuestion(id string, options ...db.QuestionOption) db.AgentQuestion {
	return db.AgentQuestion{
		ID:            id,
		AgentID:       "alice",
		StepID:        "review",
		TaskID:        "task-1",
		Question:      "Which environment should this deploy to?",
		Options:       options,
		AllowFreeText: true,
		Status:        db.QuestionPending,
		CreatedAt:     time.Now().Add(-2 * time.Minute),
	}
}

// A blocked agent must not wait for the operator to find the Tasks tab: with
// nothing else on screen the oldest question opens itself.
func TestQuestionAutoOpensOldestWhenIdle(t *testing.T) {
	a := newTestApp(100, 40)
	newer := sampleQuestion("q-new", db.QuestionOption{Value: "main"})
	older := sampleQuestion("q-old", db.QuestionOption{Value: "main"})
	older.CreatedAt = time.Now().Add(-time.Hour)

	a.Update(questionsMsg{questions: []db.AgentQuestion{newer, older}})

	if !a.model.questionActive {
		t.Fatal("a pending question should open when the dashboard is idle")
	}
	if a.model.question == nil || a.model.question.ID != "q-old" {
		t.Fatalf("opened %v, want the oldest question", a.model.question)
	}
	if got := a.model.questionAnswer; got != "main" {
		t.Fatalf("answer = %q, want the first option preselected", got)
	}
}

// Auto-open never takes the keyboard from an overlay or a half-typed filter. The
// footer badge is the substitute, and `?` opens the queue on demand.
func TestQuestionDoesNotAutoOpenOverAnOverlay(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*App)
	}{
		{"approval sheet", func(a *App) { a.model.approvalActive = true }},
		{"workflow picker", func(a *App) { a.model.pickerActive = true }},
		{"confirm modal", func(a *App) { a.model.confirmAction = "restart" }},
		{"task filter", func(a *App) {
			a.model.activeTab = 1
			a.model.tasksTab.FilterActive = true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(100, 40)
			tc.setup(a)
			a.Update(questionsMsg{questions: []db.AgentQuestion{sampleQuestion("q-1")}})

			if a.model.questionActive {
				t.Fatal("a question stole focus from an active overlay")
			}
			out := stripANSI(a.renderFooter())
			if !strings.Contains(out, "? 1 question") {
				t.Fatalf("footer is missing the question badge:\n%s", out)
			}
		})
	}
}

// `?` is the escape hatch while an overlay is busy, and cycles the queue once the
// sheet is already open.
func TestQuestionKeyOpensTheQueue(t *testing.T) {
	a := newTestApp(100, 40)
	a.model.pendingQuestions = []db.AgentQuestion{sampleQuestion("q-2"), sampleQuestion("q-1")}
	a.model.pickerActive = true

	a.handleKeyMsg(keyPress("?"))
	if !a.model.questionActive || a.model.question.ID != "q-1" {
		t.Fatalf("? should open the oldest question, got %v", a.model.question)
	}
	a.handleKeyMsg(keyPress("?"))
	if a.model.question.ID != "q-2" {
		t.Fatalf("? should cycle to the next question, got %s", a.model.question.ID)
	}
}

// The `?` shortcut must not eat a question mark being typed into an approval
// reason; it only opens the queue when `?` is not text.
func TestQuestionKeyDoesNotStealApprovalText(t *testing.T) {
	a := newTestApp(100, 40)
	a.model.pendingQuestions = []db.AgentQuestion{sampleQuestion("q-1")}
	a.openApprovalForm(&db.ApprovalRequest{ID: "wf:gate"})
	a.model.approvalFocus = approvalFocusReason

	a.handleKeyMsg(keyPress("?"))
	if a.model.questionActive {
		t.Fatal("? opened the question sheet while it was text in the reason")
	}
	if got := a.model.approvalReason; got != "?" {
		t.Fatalf("approval reason = %q, want the typed ?", got)
	}
}

// Moving the option cursor selects the answer; digits and arrows do the same.
func TestQuestionOptionSelection(t *testing.T) {
	a := newTestApp(100, 40)
	a.openQuestion(sampleQuestion("q-1",
		db.QuestionOption{Value: "staging", Label: "Staging"},
		db.QuestionOption{Value: "production", Label: "Production"},
	))

	if got := a.model.questionAnswer; got != "staging" {
		t.Fatalf("answer = %q, want the first option", got)
	}
	a.handleQuestionSheetKey("down")
	if got := a.model.questionAnswer; got != "production" {
		t.Fatalf("down chose %q, want production", got)
	}
	a.handleQuestionSheetKey("up")
	if got := a.model.questionAnswer; got != "staging" {
		t.Fatalf("up chose %q, want staging", got)
	}
	a.handleQuestionSheetKey("2")
	if got := a.model.questionAnswer; got != "production" {
		t.Fatalf("2 chose %q, want production", got)
	}
	a.handleQuestionSheetKey("right")
	if got := a.model.questionAnswer; got != "staging" {
		t.Fatalf("right wrapped to %q, want staging", got)
	}
	a.handleQuestionSheetKey("left")
	if got := a.model.questionAnswer; got != "production" {
		t.Fatalf("left wrapped to %q, want production", got)
	}
}

// Typing an answer overrides the chosen option, and the optional note is posted
// as the reason — all of it in one payload.
func TestQuestionAnswerOverridesOptionAndNoteBecomesReason(t *testing.T) {
	sock, captured := startQuestionSocket(t, http.StatusOK, `{"recorded":true}`)
	a := newTestApp(100, 40)
	a.socketPath = sock
	q := sampleQuestion("q-1",
		db.QuestionOption{Value: "staging", Label: "Staging"},
		db.QuestionOption{Value: "production", Label: "Production"})
	a.openQuestion(q)
	a.model.pendingQuestions = []db.AgentQuestion{q}

	a.handleQuestionSheetKey("2")   // pick production
	a.handleQuestionSheetKey("tab") // the Answer row
	for _, k := range strings.Split("use blue", "") {
		a.handleQuestionSheetKey(k)
	}
	a.handleQuestionSheetKey("tab") // the Note row
	for _, k := range strings.Split("because", "") {
		a.handleQuestionSheetKey(k)
	}
	if got := a.model.questionOption; got != -1 {
		t.Fatalf("questionOption = %d, want -1 once text overrides the radio", got)
	}

	_, cmd := a.handleQuestionSheetKey("enter")
	if cmd == nil {
		t.Fatal("enter should submit the answer")
	}
	a.Update(cmd())

	payload := <-captured
	if payload["answer"] != "use blue" {
		t.Fatalf("answer = %v, want the typed text", payload["answer"])
	}
	if payload["reason"] != "because" {
		t.Fatalf("reason = %v, want the note", payload["reason"])
	}
	if payload["actor"] == "" || payload["actor"] == nil {
		t.Fatal("actor should identify who answered")
	}
	if a.model.questionActive {
		t.Fatal("a confirmed answer should close the sheet")
	}
	if len(a.model.pendingQuestions) != 0 {
		t.Fatalf("answered question still pending: %+v", a.model.pendingQuestions)
	}
	if a.model.notice == "" || a.model.noticeIsErr {
		t.Fatalf("expected a success notice, got %q (err=%v)", a.model.notice, a.model.noticeIsErr)
	}
}

// A failed submit keeps the question on screen so the operator can retry, and
// shows the daemon's own message.
func TestQuestionSubmitFailureKeepsTheQuestion(t *testing.T) {
	sock, _ := startQuestionSocket(t, http.StatusInternalServerError, "store unavailable")
	a := newTestApp(100, 40)
	a.socketPath = sock
	q := sampleQuestion("q-1", db.QuestionOption{Value: "yes"})
	a.openQuestion(q)
	a.model.pendingQuestions = []db.AgentQuestion{q}

	_, cmd := a.handleQuestionSheetKey("enter")
	a.Update(cmd())

	if !a.model.questionActive || a.model.question == nil || a.model.question.ID != "q-1" {
		t.Fatal("a failed submit must leave the question open")
	}
	if !strings.Contains(a.model.questionErr, "store unavailable") {
		t.Fatalf("questionErr = %q, want the daemon's message", a.model.questionErr)
	}
	if len(a.model.pendingQuestions) != 1 {
		t.Fatal("a failed submit must not drop the question from the queue")
	}
}

// 409 means the row was answered or expired elsewhere; it must leave the queue
// and say so rather than offer a retry that can never succeed.
func TestQuestionConflictDropsIt(t *testing.T) {
	sock, _ := startQuestionSocket(t, http.StatusConflict,
		"question q-1 is already answered")
	a := newTestApp(100, 40)
	a.socketPath = sock
	q := sampleQuestion("q-1", db.QuestionOption{Value: "yes"})
	a.openQuestion(q)
	a.model.pendingQuestions = []db.AgentQuestion{q}

	_, cmd := a.handleQuestionSheetKey("enter")
	a.Update(cmd())

	if a.model.questionActive {
		t.Fatal("a 409 should close the sheet: the question is gone")
	}
	if len(a.model.pendingQuestions) != 0 {
		t.Fatal("a 409 should remove the question from the queue")
	}
	if !a.model.noticeIsErr || !strings.Contains(a.model.notice, "already answered") {
		t.Fatalf("notice = %q (err=%v), want the daemon's conflict message", a.model.notice, a.model.noticeIsErr)
	}
}

// A question past its deadline is not answerable, so the poll drops it before it
// can reach the sheet — including when the store's lazy sweep has not run yet.
func TestQuestionExpiredRowsAreSkipped(t *testing.T) {
	past := time.Now().Add(-time.Second)
	future := time.Now().Add(time.Hour)
	now := time.Now()
	got := liveQuestions([]db.AgentQuestion{
		{ID: "live"},
		{ID: "deadline-ok", ExpiresAt: &future},
		{ID: "dead", ExpiresAt: &past},
	}, now)
	if len(got) != 2 || got[0].ID != "live" || got[1].ID != "deadline-ok" {
		t.Fatalf("liveQuestions = %+v, want the two unexpired rows", got)
	}
}

// Esc postpones. It must not come back on its own: an auto-open that ignores
// the dismissal would reopen the same modal every two seconds, so "later" would
// mean "immediately" and there would be no way to work around it. `?` reopens.
func TestQuestionEscSnoozesUntilAsked(t *testing.T) {
	q := sampleQuestion("q-1", db.QuestionOption{Value: "yes"})
	a := newTestApp(100, 40)
	a.openQuestion(q)
	a.model.pendingQuestions = []db.AgentQuestion{q}

	a.handleQuestionSheetKey("esc")
	if a.model.questionActive {
		t.Fatal("esc should close the sheet")
	}
	if len(a.model.pendingQuestions) != 1 {
		t.Fatal("esc must leave the question pending")
	}
	if !a.model.questionSnoozed[q.ID] {
		t.Fatal("esc should remember that the operator put this question aside")
	}
	if !strings.Contains(a.model.notice, "press ?") {
		t.Fatalf("notice = %q, want it to name the way back", a.model.notice)
	}

	a.Update(questionsMsg{questions: []db.AgentQuestion{q}})
	if a.model.questionActive {
		t.Fatal("a question the operator put aside must not reopen by itself")
	}

	a.openNextQuestion()
	if !a.model.questionActive {
		t.Fatal("? should reopen the question the operator put aside")
	}
}

func TestQuestionSheetRendersContextAndControls(t *testing.T) {
	a := newTestApp(100, 40)
	a.openQuestion(sampleQuestion("q-1",
		db.QuestionOption{Value: "staging", Label: "Staging"},
		db.QuestionOption{Value: "production", Label: "Production"},
	))

	out := stripANSI(a.renderQuestionSheet(""))
	for _, want := range []string{
		"Answer question", "agent alice", "step review", "task task-1", "ago",
		"Options", "Staging", "Production", "Answer", "Note (optional)",
		"submit", "esc", "reopen",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("sheet is missing %q:\n%s", want, out)
		}
	}
}
