package daemon

import (
	"crypto/subtle"
	"net"
	"net/http"
	"os"
	"strings"
)

// connKey is the context key for the raw net.Conn injected by the HTTP
// server's ConnContext hook, so the auth middleware can inspect SO_PEERCRED.
type connKey struct{}

// controlAuth is an HTTP middleware that enforces authentication on every
// mutating (non-GET/HEAD/OPTIONS) control-plane request.
//
// A request is allowed when ANY of the following holds:
//
//  1. The method is read-only (GET, HEAD, OPTIONS).
//  2. The peer process has the same effective UID as the daemon via
//     SO_PEERCRED (Linux only; skipped on other platforms).
//  3. The Authorization header carries the correct Bearer token.
func controlAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// (1) Read-only methods pass without auth.
		switch r.Method {
		case "", http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		// (2) SO_PEERCRED shortcut: same UID as the daemon is trusted implicitly.
		if conn, _ := r.Context().Value(connKey{}).(net.Conn); conn != nil {
			if uid, ok := peerUID(conn); ok && uid == uint32(os.Getuid()) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// (3) Bearer token check (constant-time comparison).
		// Require the "Bearer " scheme prefix to avoid accepting the raw token.
		authHeader := r.Header.Get("Authorization")
		bearer, ok := strings.CutPrefix(authHeader, "Bearer ")
		if ok && subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}
