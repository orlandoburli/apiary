package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/model"
	"github.com/orlandoburli/apiary/internal/source"
)

func newAdapter(t *testing.T, cfg map[string]any) *Adapter {
	t.Helper()
	a := &Adapter{}
	a.SetID("hooks")
	if err := a.Connect(context.Background(), cfg); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return a
}

func deliver(t *testing.T, a *Adapter, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	a.WebhookHandler().ServeHTTP(rec, req)
	return rec
}

func bearerReq(body, token string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhook/hooks", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestRegistered(t *testing.T) {
	a, ok := source.New("webhook")
	if !ok {
		t.Fatal("webhook adapter not registered")
	}
	if a.WebhookHandler() == nil {
		t.Error("WebhookHandler must be non-nil on a fresh instance (push-capability probe)")
	}
}

func TestConnectValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"missing secret", map[string]any{}},
		{"missing secret hmac", map[string]any{"auth": "hmac"}},
		{"bad auth", map[string]any{"secret": "s", "auth": "basic"}},
		{"bad format", map[string]any{"secret": "s", "format": "xml"}},
		{"bad max_pending", map[string]any{"secret": "s", "max_pending": 0}},
		{"bad max_body_bytes", map[string]any{"secret": "s", "max_body_bytes": -1}},
		{"bad tolerance", map[string]any{"secret": "s", "tolerance": "soon"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{}
			if err := a.Connect(context.Background(), tc.cfg); err == nil {
				t.Errorf("Connect(%v) should fail", tc.cfg)
			}
		})
	}

	// auth: none needs no secret.
	a := &Adapter{}
	if err := a.Connect(context.Background(), map[string]any{"auth": "none"}); err != nil {
		t.Errorf("auth none without secret should connect: %v", err)
	}
}

func TestBearerAuth(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok"})

	if rec := deliver(t, a, bearerReq(`{"title":"x"}`, "tok")); rec.Code != http.StatusAccepted {
		t.Errorf("valid token: got %d, want 202 (%s)", rec.Code, rec.Body)
	}
	if rec := deliver(t, a, bearerReq(`{"title":"x"}`, "wrong")); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", rec.Code)
	}
	noAuth := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	if rec := deliver(t, a, noAuth); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing header: got %d, want 401", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if rec := deliver(t, a, req); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: got %d, want 405", rec.Code)
	}
}

func TestBodyTooLarge(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok", "max_body_bytes": 10})
	if rec := deliver(t, a, bearerReq(`{"title":"way past ten bytes"}`, "tok")); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: got %d, want 413", rec.Code)
	}
}

func TestBadJSON(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok"})
	if rec := deliver(t, a, bearerReq(`{nope`, "tok")); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON: got %d, want 400", rec.Code)
	}
}

func signed(body, secret string, ts time.Time) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsStr + "." + body))
	req.Header.Set(headerTimestamp, tsStr)
	req.Header.Set(headerSignature, signaturePrefix+hex.EncodeToString(mac.Sum(nil)))
	return req
}

func TestHMACAuth(t *testing.T) {
	now := time.Now()
	a := newAdapter(t, map[string]any{"secret": "s3cr3t", "auth": "hmac"})
	a.now = func() time.Time { return now }

	body := `{"id":"e1","title":"x"}`
	if rec := deliver(t, a, signed(body, "s3cr3t", now)); rec.Code != http.StatusAccepted {
		t.Errorf("valid signature: got %d, want 202 (%s)", rec.Code, rec.Body)
	}

	// Replay of the identical signed request is rejected.
	if rec := deliver(t, a, signed(body, "s3cr3t", now)); rec.Code != http.StatusUnauthorized {
		t.Errorf("replayed signature: got %d, want 401", rec.Code)
	}

	if rec := deliver(t, a, signed(body, "wrong-secret", now)); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong secret: got %d, want 401", rec.Code)
	}

	// Timestamp outside the tolerance window.
	if rec := deliver(t, a, signed(body, "s3cr3t", now.Add(-10*time.Minute))); rec.Code != http.StatusUnauthorized {
		t.Errorf("stale timestamp: got %d, want 401", rec.Code)
	}

	// Tampered body under a valid signature.
	req := signed(body, "s3cr3t", now.Add(time.Second))
	req.Body = http.NoBody
	if rec := deliver(t, a, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("tampered body: got %d, want 401", rec.Code)
	}

	// Missing headers.
	bare := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	if rec := deliver(t, a, bare); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing signature headers: got %d, want 401", rec.Code)
	}
}

func TestEnqueuePollDrain(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok"})

	deliver(t, a, bearerReq(`{"id":"e1","title":"first"}`, "tok"))
	deliver(t, a, bearerReq(`{"id":"e2","title":"second"}`, "tok"))
	// Duplicate delivery of a queued event is dropped silently.
	if rec := deliver(t, a, bearerReq(`{"id":"e1","title":"first"}`, "tok")); rec.Code != http.StatusAccepted {
		t.Errorf("duplicate delivery: got %d, want 202", rec.Code)
	}

	items, err := a.Poll(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != "e1" || items[1].ID != "e2" {
		t.Errorf("order/ids wrong: %q, %q", items[0].ID, items[1].ID)
	}
	if items[0].SourceID != "hooks" {
		t.Errorf("SourceID = %q, want hooks", items[0].SourceID)
	}

	// Drained: next poll is empty, and the same event may be re-delivered
	// (downstream task/instance dedup owns re-dispatch prevention).
	if items, _ := a.Poll(context.Background(), time.Time{}); len(items) != 0 {
		t.Errorf("second poll returned %d items, want 0", len(items))
	}
	if rec := deliver(t, a, bearerReq(`{"id":"e1","title":"first"}`, "tok")); rec.Code != http.StatusAccepted {
		t.Errorf("re-delivery after drain: got %d, want 202", rec.Code)
	}
}

func TestQueueOverflow(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok", "max_pending": 2})

	for i := 0; i < 2; i++ {
		deliver(t, a, bearerReq(fmt.Sprintf(`{"id":"e%d"}`, i), "tok"))
	}
	if rec := deliver(t, a, bearerReq(`{"id":"overflow"}`, "tok")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("overflow: got %d, want 429", rec.Code)
	}

	// Draining frees capacity.
	_, _ = a.Poll(context.Background(), time.Time{})
	if rec := deliver(t, a, bearerReq(`{"id":"overflow"}`, "tok")); rec.Code != http.StatusAccepted {
		t.Errorf("after drain: got %d, want 202", rec.Code)
	}
}

func TestWake(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok"})
	woke := 0
	a.SetWake(func() { woke++ })

	deliver(t, a, bearerReq(`{"id":"e1"}`, "tok"))
	if woke != 1 {
		t.Errorf("wake called %d times, want 1", woke)
	}
	// A delivery that enqueues nothing (duplicate) does not wake.
	deliver(t, a, bearerReq(`{"id":"e1"}`, "tok"))
	if woke != 1 {
		t.Errorf("wake after duplicate: %d, want 1", woke)
	}
}

func TestFilters(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok"})
	a.SetFilters([]string{"open"}, []string{"team=sre"})

	deliver(t, a, bearerReq(`{"id":"match","labels":{"team":"sre"}}`, "tok"))
	deliver(t, a, bearerReq(`{"id":"wrong-label","labels":{"team":"web"}}`, "tok"))
	deliver(t, a, bearerReq(`{"id":"wrong-state","state":"resolved","labels":{"team":"sre"}}`, "tok"))

	items, _ := a.Poll(context.Background(), time.Time{})
	if len(items) != 1 || items[0].ID != "match" {
		t.Fatalf("filters kept %d items (want just \"match\"): %+v", len(items), items)
	}
}

func TestNoOpsAndCaps(t *testing.T) {
	a := newAdapter(t, map[string]any{"secret": "tok"})
	if err := a.Acknowledge(context.Background(), model.SourceItem{ID: "e1"}, "dispatched"); err != nil {
		t.Errorf("Acknowledge: %v", err)
	}
	if err := a.WriteResult(context.Background(), model.SourceItem{ID: "e1"}, model.RunResult{Success: true}); err != nil {
		t.Errorf("WriteResult: %v", err)
	}

	// Read-only: none of the optional write capabilities may be implemented,
	// or config validation would wrongly allow write features on this source.
	var s source.Adapter = a
	if _, ok := s.(source.StateSetter); ok {
		t.Error("webhook must not implement StateSetter")
	}
	if _, ok := s.(source.LabelAdder); ok {
		t.Error("webhook must not implement LabelAdder")
	}
	if _, ok := s.(source.TaskPoller); ok {
		t.Error("webhook must not implement TaskPoller")
	}
	if _, ok := s.(source.CIStatusPoller); ok {
		t.Error("webhook must not implement CIStatusPoller")
	}
	if _, ok := s.(source.SubIssueCreator); ok {
		t.Error("webhook must not implement SubIssueCreator")
	}
}
