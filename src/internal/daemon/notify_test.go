package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/config"
	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func TestMatchedEscalationLabels(t *testing.T) {
	cfg := &config.NotificationsConfig{
		OnLabels: []string{"needs-attention", "needs-human"},
		Channels: []config.NotificationChannel{{Type: "command", Run: "true"}},
	}
	got := matchedEscalationLabels(cfg, []string{"blocked", "needs-attention"})
	if len(got) != 1 || got[0] != "needs-attention" {
		t.Fatalf("got %v", got)
	}
	if matchedEscalationLabels(nil, []string{"needs-attention"}) != nil {
		t.Fatal("nil config must match nothing")
	}
	if matchedEscalationLabels(&config.NotificationsConfig{OnLabels: []string{"x"}}, []string{"x"}) != nil {
		t.Fatal("config without channels must match nothing")
	}
}

func TestRenderNotifyCommandQuotesValues(t *testing.T) {
	ev := escalationEvent{
		Title:   `deploy failed; rm -rf / '"$(reboot)"`,
		Label:   "needs-attention",
		CellID:  "3743",
		Summary: "staging deploy exited 1",
	}
	out := renderNotifyCommand(`notify.sh {{cell_id}} {{label}} {{title}} {{summary}}`, ev)
	if strings.Contains(out, "$(reboot)") && !strings.Contains(out, `'`) {
		t.Fatalf("unquoted substitution: %s", out)
	}
	// The dangerous title must be inside single quotes with inner quotes escaped.
	if !strings.Contains(out, `'deploy failed; rm -rf / '\''"$(reboot)"'`) {
		t.Fatalf("title not safely quoted: %s", out)
	}
	if !strings.Contains(out, "'3743' 'needs-attention'") {
		t.Fatalf("simple values not substituted: %s", out)
	}
}

func TestNotifyEscalationRunsCommand(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.txt")
	d := &Dispatcher{cfg: &config.Config{
		Notifications: &config.NotificationsConfig{
			OnLabels: []string{"needs-attention"},
			Channels: []config.NotificationChannel{{
				Type: "command",
				Run:  `printf '%s|%s|%s' {{cell_id}} {{label}} "$APIARY_TITLE" > ` + outFile,
			}},
		},
	}}
	d.notifyEscalation(escalationEvent{
		CellID: "owner/repo#42",
		Label:  "needs-attention",
		Title:  "deploy failed",
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(outFile)
		if err == nil && len(data) > 0 {
			if got, want := string(data), "owner/repo#42|needs-attention|deploy failed"; got != want {
				t.Fatalf("got %q want %q", got, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("notification command never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The full path issue #201 describes: a hook parks a task with
// needs-attention → the configured channel fires with the item's identity.
func TestApplyHookFiresEscalationNotification(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.txt")
	fake := &fakeHookSource{}
	d := &Dispatcher{
		sources: map[string]source.Adapter{"fake": fake},
		cfg: &config.Config{
			Notifications: &config.NotificationsConfig{
				OnLabels: []string{"needs-attention"},
				Channels: []config.NotificationChannel{{
					Type: "command",
					Run:  `printf '%s %s' {{number}} {{label}} > ` + outFile,
				}},
			},
		},
	}
	task := model.InternalTask{ID: "T-9", Title: "Deploy staging"}
	bindings := []model.SourceBinding{{SourceID: "fake", SourceItemID: "I-9", SourceItemNumber: "ERP-42"}}
	hook := config.OnComplete{AddLabels: []string{"needs-attention"}}

	if err := (&wfSideEffects{d: d}).ApplyHook(context.Background(), task, bindings, hook); err != nil {
		t.Fatalf("ApplyHook: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(outFile)
		if string(data) == "ERP-42 needs-attention" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("notification never fired, got %q", string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestValidateNotifications(t *testing.T) {
	c := &config.Config{
		Version: "1",
		Notifications: &config.NotificationsConfig{
			Channels: []config.NotificationChannel{{Type: "slack"}, {Type: "command", Run: " "}},
		},
	}
	errs := c.Validate()
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		"notifications: on_labels is required",
		`unknown type "slack"`,
		`run is required for type "command"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing validation error %q in:\n%s", want, all)
		}
	}
}
