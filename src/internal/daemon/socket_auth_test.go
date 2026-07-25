package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSocketAuthMiddlewareAllowsGET(t *testing.T) {
	token := "testtoken"
	handler := socketAuthMiddleware(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// GET without any Authorization header must pass through.
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET without token: expected 200, got %d", w.Code)
	}
}

func TestSocketAuthMiddlewareBlocksMutatingWithoutToken(t *testing.T) {
	token := "testtoken"
	handler := socketAuthMiddleware(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, "/restart/cell-1", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token: expected 401, got %d", method, w.Code)
		}
	}
}

func TestSocketAuthMiddlewareBlocksWrongToken(t *testing.T) {
	token := "correcttoken"
	handler := socketAuthMiddleware(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/restart/cell-1", nil)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST with wrong token: expected 401, got %d", w.Code)
	}
}

func TestSocketAuthMiddlewareAllowsCorrectToken(t *testing.T) {
	token := "correcttoken"
	handler := socketAuthMiddleware(token, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/restart/cell-1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST with correct token: expected 200, got %d", w.Code)
	}
}

func TestWriteAndReadSocketToken(t *testing.T) {
	dir := t.TempDir()

	token, err := writeSocketToken(dir)
	if err != nil {
		t.Fatalf("writeSocketToken: %v", err)
	}
	if len(token) != 64 { // 32 bytes → 64 hex chars
		t.Fatalf("unexpected token length %d", len(token))
	}

	got, err := ReadSocketToken(dir)
	if err != nil {
		t.Fatalf("ReadSocketToken: %v", err)
	}
	if got != token {
		t.Fatalf("token mismatch: wrote %q, read %q", token, got)
	}
}

func TestSocketAuthMiddlewareDashboardApprovalRequiresToken(t *testing.T) {
	token := "securetoken"
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	})
	handler := socketAuthMiddleware(token, inner)

	// Dashboard approval path without token must be rejected.
	req := httptest.NewRequest(http.MethodPost, "/approvals/req-1/respond", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("approval without token: expected 401, got %d", w.Code)
	}
	if called {
		t.Fatal("inner handler should not have been called without token")
	}

	// With correct token it must reach the inner handler.
	req2 := httptest.NewRequest(http.MethodPost, "/approvals/req-1/respond", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("approval with token: expected 202, got %d", w2.Code)
	}
}
