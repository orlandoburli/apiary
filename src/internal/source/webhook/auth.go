package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Signature headers for auth: "hmac". The sender computes
// HMAC-SHA256(secret, "<timestamp>.<raw body>") and sends:
//
//	X-Apiary-Timestamp: <unix seconds>
//	X-Apiary-Signature: sha256=<hex digest>
//
// The timestamp is bound into the signature so a captured delivery cannot be
// replayed outside the tolerance window, and signatures seen inside the
// window are cached and rejected on reuse.
const (
	headerTimestamp = "X-Apiary-Timestamp"
	headerSignature = "X-Apiary-Signature"
	signaturePrefix = "sha256="
)

// readBody reads at most maxBytes of the request body.
func readBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBodyBytes
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("body exceeds %d bytes", maxBytes)
	}
	return body, nil
}

// authenticate verifies a delivery per the configured auth mode.
func (a *Adapter) authenticate(r *http.Request, body []byte) error {
	switch a.authMode {
	case "none":
		return nil
	case "bearer":
		return a.checkBearer(r)
	case "hmac":
		return a.checkHMAC(r, body)
	}
	return fmt.Errorf("unknown auth mode %q", a.authMode)
}

func (a *Adapter) checkBearer(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		return fmt.Errorf("missing Authorization: Bearer header")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.secret)) != 1 {
		return fmt.Errorf("bearer token mismatch")
	}
	return nil
}

func (a *Adapter) checkHMAC(r *http.Request, body []byte) error {
	sig, ok := strings.CutPrefix(r.Header.Get(headerSignature), signaturePrefix)
	if !ok {
		return fmt.Errorf("missing %s: %s<hex> header", headerSignature, signaturePrefix)
	}
	given, err := hex.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("%s is not hex", headerSignature)
	}

	tsHeader := r.Header.Get(headerTimestamp)
	tsSec, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("missing or invalid %s header (unix seconds)", headerTimestamp)
	}
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}
	ts := time.Unix(tsSec, 0)
	if diff := now.Sub(ts); diff > a.tolerance || diff < -a.tolerance {
		return fmt.Errorf("timestamp outside the ±%s tolerance window", a.tolerance)
	}

	mac := hmac.New(sha256.New, []byte(a.secret))
	mac.Write([]byte(tsHeader))
	mac.Write([]byte("."))
	mac.Write(body)
	if !hmac.Equal(given, mac.Sum(nil)) {
		return fmt.Errorf("signature mismatch")
	}

	// Replay guard: a valid signature is single-use within the tolerance
	// window (after the window the timestamp check alone rejects it).
	a.mu.Lock()
	defer a.mu.Unlock()
	for s, exp := range a.seen {
		if now.After(exp) {
			delete(a.seen, s)
		}
	}
	if _, replayed := a.seen[sig]; replayed {
		return fmt.Errorf("replayed signature")
	}
	a.seen[sig] = now.Add(a.tolerance)
	return nil
}
