package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// newClientServer builds a test server whose handler sees every request and a
// client pointed at it.
func newClientServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, newClient(srv.URL, "", "", "")
}

func TestClientSendsBearerToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	c := newClient(srv.URL, "sekret", "", "")
	if _, err := c.alerts(context.Background(), nil); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if got != "Bearer sekret" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer sekret")
	}
}

func TestClientSendsBasicAuth(t *testing.T) {
	var user, pass string
	var ok bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok = r.BasicAuth()
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	c := newClient(srv.URL, "", "alice", "hunter2")
	if _, err := c.alerts(context.Background(), nil); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if !ok || user != "alice" || pass != "hunter2" {
		t.Errorf("basic auth = (%q, %q, ok=%v), want (alice, hunter2, true)", user, pass, ok)
	}
}

func TestClientBearerTakesPrecedenceOverBasic(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	c := newClient(srv.URL, "tok", "alice", "hunter2")
	if _, err := c.alerts(context.Background(), nil); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if got != "Bearer tok" {
		t.Errorf("Authorization = %q, want bearer to win over basic auth", got)
	}
}

func TestClientRetriesOn5xxThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.Header().Set("Retry-After", "0") // keep the test fast
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`[{"fingerprint":"f1","labels":{"alertname":"A"}}]`))
	})

	alerts, err := c.alerts(context.Background(), nil)
	if err != nil {
		t.Fatalf("alerts after retries: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Fingerprint != "f1" {
		t.Errorf("alerts = %+v, want the one recovered alert", alerts)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("server saw %d requests, want 3 (2 failures + 1 success)", n)
	}
}

func TestClientRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`[]`))
	})

	if _, err := c.alerts(context.Background(), nil); err != nil {
		t.Fatalf("alerts after 429: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("server saw %d requests, want 2", n)
	}
}

func TestClientRetryExhausted(t *testing.T) {
	var calls atomic.Int32
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := c.alerts(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "exceeded 3 retries") {
		t.Errorf("error = %v, want mention of exhausted retries", err)
	}
	if n := calls.Load(); n != int32(maxRetries) {
		t.Errorf("server saw %d requests, want %d", n, maxRetries)
	}
}

func TestClientDoesNotRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad matcher", http.StatusBadRequest)
	})

	_, err := c.alerts(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Errorf("error = %v, want status 400", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("server saw %d requests, want exactly 1 (4xx must not retry)", n)
	}
}

func TestClientRejectsMalformedJSON(t *testing.T) {
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"not":"an array"`))
	})

	_, err := c.alerts(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "decoding alerts response") {
		t.Errorf("error = %v, want decode failure", err)
	}
}

func TestClientContextCancelStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		cancel() // cancel while the first attempt is in flight
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := c.alerts(ctx, nil); err == nil {
		t.Fatal("expected error when context is cancelled during retries")
	}
}
