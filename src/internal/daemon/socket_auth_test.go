package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestControlAuth(t *testing.T) {
	const token = "test-secret-token"
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	protected := controlAuth(token, okHandler)

	cases := []struct {
		name   string
		method string
		auth   string
		want   int
	}{
		// Read-only methods always pass.
		{"GET no auth", http.MethodGet, "", http.StatusOK},
		{"HEAD no auth", http.MethodHead, "", http.StatusOK},
		{"OPTIONS no auth", http.MethodOptions, "", http.StatusOK},
		// Mutating methods without token are rejected.
		{"POST no auth", http.MethodPost, "", http.StatusUnauthorized},
		{"PATCH no auth", http.MethodPatch, "", http.StatusUnauthorized},
		{"DELETE no auth", http.MethodDelete, "", http.StatusUnauthorized},
		// Mutating methods with wrong token are rejected.
		{"POST wrong token", http.MethodPost, "Bearer wrong", http.StatusUnauthorized},
		{"POST bare token no prefix", http.MethodPost, token, http.StatusUnauthorized},
		// Mutating methods with correct Bearer token pass.
		{"POST correct token", http.MethodPost, "Bearer " + token, http.StatusOK},
		{"PATCH correct token", http.MethodPatch, "Bearer " + token, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/test", nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rr := httptest.NewRecorder()
			protected.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestSocketToken(t *testing.T) {
	dir := t.TempDir()

	// First call: creates a new token file.
	tok1, err := LoadOrCreateSocketToken(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok1 == "" {
		t.Fatal("expected non-empty token")
	}

	// Second call: returns the same token from file.
	tok2, err := LoadOrCreateSocketToken(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok1 != tok2 {
		t.Fatalf("token mismatch: %q != %q", tok1, tok2)
	}

	// ReadSocketToken: returns the same token.
	tok3, err := ReadSocketToken(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok3 != tok1 {
		t.Fatalf("read token mismatch: %q != %q", tok3, tok1)
	}

	// Config override: returns the provided secret without touching the file.
	tok4, err := LoadOrCreateSocketToken(dir, "my-override")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok4 != "my-override" {
		t.Fatalf("expected override, got %q", tok4)
	}
	// File on disk should still hold the old auto-generated token.
	tok5, _ := ReadSocketToken(dir)
	if tok5 != tok1 {
		t.Fatalf("file should be unchanged after config override")
	}
}
