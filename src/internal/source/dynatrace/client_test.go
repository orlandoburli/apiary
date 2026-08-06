package dynatrace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newClientServer builds a test server whose handler sees every request and a
// client pointed at it.
func newClientServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, newClient(srv.URL, "dt0c01.sekret")
}

func emptyPage(w http.ResponseWriter) {
	json.NewEncoder(w).Encode(map[string]any{"totalCount": 0, "pageSize": 500, "problems": []any{}})
}

func TestClientSendsAPIToken(t *testing.T) {
	var got string
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		emptyPage(w)
	})

	if _, err := c.problems(context.Background(), "", time.Now()); err != nil {
		t.Fatalf("problems: %v", err)
	}
	if got != "Api-Token dt0c01.sekret" {
		t.Errorf("Authorization = %q, want %q", got, "Api-Token dt0c01.sekret")
	}
}

func TestClientFollowsPagination(t *testing.T) {
	var calls atomic.Int32
	var secondQuery map[string][]string
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"totalCount":  2,
				"pageSize":    1,
				"nextPageKey": "KEY2",
				"problems":    []map[string]any{{"problemId": "pid-1"}},
			})
			return
		}
		secondQuery = r.URL.Query()
		json.NewEncoder(w).Encode(map[string]any{
			"totalCount": 2,
			"pageSize":   1,
			"problems":   []map[string]any{{"problemId": "pid-2"}},
		})
	})

	problems, err := c.problems(context.Background(), `status("open")`, time.Now())
	if err != nil {
		t.Fatalf("problems: %v", err)
	}
	if len(problems) != 2 || problems[0].ProblemID != "pid-1" || problems[1].ProblemID != "pid-2" {
		t.Errorf("problems = %+v, want both pages in order", problems)
	}
	if got := secondQuery["nextPageKey"]; len(got) != 1 || got[0] != "KEY2" {
		t.Errorf("page 2 nextPageKey = %v, want [KEY2]", secondQuery["nextPageKey"])
	}
	// Follow-up pages must carry ONLY nextPageKey — the API rejects repeats of
	// the original params.
	for _, banned := range []string{"problemSelector", "from", "pageSize"} {
		if _, ok := secondQuery[banned]; ok {
			t.Errorf("page 2 still carries %s param: %v", banned, secondQuery[banned])
		}
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
		json.NewEncoder(w).Encode(map[string]any{"problems": []map[string]any{{"problemId": "pid-1"}}})
	})

	problems, err := c.problems(context.Background(), "", time.Now())
	if err != nil {
		t.Fatalf("problems after retries: %v", err)
	}
	if len(problems) != 1 || problems[0].ProblemID != "pid-1" {
		t.Errorf("problems = %+v, want the one recovered problem", problems)
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
		emptyPage(w)
	})

	if _, err := c.problems(context.Background(), "", time.Now()); err != nil {
		t.Fatalf("problems after 429: %v", err)
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

	_, err := c.problems(context.Background(), "", time.Now())
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
		http.Error(w, "invalid selector", http.StatusBadRequest)
	})

	_, err := c.problems(context.Background(), "", time.Now())
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
		w.Write([]byte(`{"problems": [`))
	})

	_, err := c.problems(context.Background(), "", time.Now())
	if err == nil || !strings.Contains(err.Error(), "decoding problems response") {
		t.Errorf("error = %v, want decode failure", err)
	}
}

func TestClientContextCancelStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, c := newClientServer(t, func(w http.ResponseWriter, r *http.Request) {
		cancel() // cancel while the first attempt is in flight
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := c.problems(ctx, "", time.Now()); err == nil {
		t.Fatal("expected error when context is cancelled during retries")
	}
}
