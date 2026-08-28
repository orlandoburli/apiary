package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func indexJSON(t *testing.T) []byte {
	t.Helper()
	index := Index{SchemaVersion: 1, Plugins: []RegistryPlugin{*testEntry(testRelease("1.0.0", ">= 0.1.0-0"))}}
	encoded, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestValidateRegistryURLRejectsPlaintextAndUnknownSchemes(t *testing.T) {
	if err := ValidateRegistryURL("http://example.invalid/index.json"); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("plaintext resolution must be refused, got %v", err)
	}
	if err := ValidateRegistryURL("ftp://example.invalid/index.json"); err == nil {
		t.Fatal("unknown schemes must be refused")
	}
	if err := ValidateRegistryURL("https://example.invalid/index.json"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRegistryURL("file:///tmp/index.json"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIndexFromFileURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(path, indexJSON(t), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIndex(context.Background(), "file://"+path, FetchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Plugins) != 1 || loaded.FromCache {
		t.Fatalf("want one plugin read directly, got %d plugins fromCache=%v", len(loaded.Plugins), loaded.FromCache)
	}
}

// The second run must revalidate rather than re-download, and a 304 must serve
// the cached body.
func TestLoadIndexUsesConditionalGet(t *testing.T) {
	body := indexJSON(t)
	var requests, conditional atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()

	cache := t.TempDir()
	client := server.Client()
	first, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if first.FromCache {
		t.Fatal("the first fetch cannot come from a cold cache")
	}
	second, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if !second.FromCache || conditional.Load() != 1 || requests.Load() != 2 {
		t.Fatalf("want a conditional revalidation: fromCache=%v conditional=%d requests=%d", second.FromCache, conditional.Load(), requests.Load())
	}
}

func TestLoadIndexOfflineNeedsAWarmCache(t *testing.T) {
	body := indexJSON(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	cache := t.TempDir()

	if _, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Offline: true, Client: server.Client()}); err == nil {
		t.Fatal("--offline with a cold cache must be an error, not an empty result")
	}
	if _, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Client: server.Client()}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Offline: true, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.FromCache {
		t.Fatal("--offline must serve the cache")
	}
}

// An unreachable registry with a warm cache still answers — but says so.
func TestLoadIndexFallsBackToCacheWithAWarning(t *testing.T) {
	body := indexJSON(t)
	var fail atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	cache := t.TempDir()
	if _, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Client: server.Client()}); err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	loaded, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.FromCache || loaded.Warning == nil {
		t.Fatalf("a cached answer during an outage must carry a warning: fromCache=%v warning=%v", loaded.FromCache, loaded.Warning)
	}
}

// A corrupt response must not be cached: the next run should get a clean fetch
// rather than inherit the failure.
func TestLoadIndexDoesNotCacheCorruptResponses(t *testing.T) {
	var serveGarbage atomic.Bool
	serveGarbage.Store(true)
	body := indexJSON(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if serveGarbage.Load() {
			_, _ = w.Write([]byte("{not json"))
			return
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	cache := t.TempDir()

	if _, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Client: server.Client()}); err == nil {
		t.Fatal("a corrupt index must be an error")
	}
	if _, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Offline: true, Client: server.Client()}); err == nil {
		t.Fatal("the corrupt response must not have been cached")
	}
	serveGarbage.Store(false)
	if _, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: cache, Client: server.Client()}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIndexRejectsUnexpectedStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer server.Close()
	_, err := LoadIndex(context.Background(), server.URL, FetchOptions{CacheDir: t.TempDir(), Client: server.Client()})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(http.StatusNotFound)) {
		t.Fatalf("want the status in the error, got %v", err)
	}
}

// signedRegistry serves an index and its .minisig from the same TLS server, the
// way a docs deploy publishes them.
func signedRegistry(t *testing.T, key *signingKey, tamper bool, withSignature bool) (string, *http.Client) {
	t.Helper()
	body := indexJSON(t)
	signature := key.sign(body, minisignAlgorithmPrehashed, "timestamp:1 file:index.json")
	if tamper {
		body = append(body[:len(body)-1], []byte(` `)...)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	if withSignature {
		mux.HandleFunc("/index.json.minisig", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(signature) })
	}
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	return server.URL + "/index.json", server.Client()
}

func TestLoadIndexVerifiesASignedRegistry(t *testing.T) {
	key := newSigningKey(t)
	url, client := signedRegistry(t, key, false, true)
	loaded, err := LoadIndex(context.Background(), url, FetchOptions{
		CacheDir: t.TempDir(), Client: client, PublicKey: key.publicKeyLine(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Unsigned {
		t.Fatal("a verified index must not be reported as unsigned")
	}
}

// Pinning a key means the index must be signed. There is no flag to proceed.
func TestLoadIndexFailsClosedWithoutASignature(t *testing.T) {
	key := newSigningKey(t)
	url, client := signedRegistry(t, key, false, false)
	_, err := LoadIndex(context.Background(), url, FetchOptions{
		CacheDir: t.TempDir(), Client: client, PublicKey: key.publicKeyLine(),
	})
	if err == nil || !strings.Contains(err.Error(), "refusing to use an unverified index") {
		t.Fatalf("want a fail-closed refusal, got %v", err)
	}
}

func TestLoadIndexRejectsAModifiedSignedIndex(t *testing.T) {
	key := newSigningKey(t)
	url, client := signedRegistry(t, key, true, true)
	_, err := LoadIndex(context.Background(), url, FetchOptions{
		CacheDir: t.TempDir(), Client: client, PublicKey: key.publicKeyLine(),
	})
	if err == nil || !strings.Contains(err.Error(), "failed signature verification") {
		t.Fatalf("want a verification failure, got %v", err)
	}
}

func TestLoadIndexRejectsAnIndexSignedByAnotherKey(t *testing.T) {
	url, client := signedRegistry(t, newSigningKey(t), false, true)
	_, err := LoadIndex(context.Background(), url, FetchOptions{
		CacheDir: t.TempDir(), Client: client, PublicKey: newSigningKey(t).publicKeyLine(),
	})
	if err == nil || !strings.Contains(err.Error(), "failed signature verification") {
		t.Fatalf("want a key mismatch, got %v", err)
	}
}

// The cache is on disk, where anything with write access can reach it, so a
// cached index is verified on the way out as well as on the way in.
func TestLoadIndexVerifiesTheCacheToo(t *testing.T) {
	key := newSigningKey(t)
	url, client := signedRegistry(t, key, false, true)
	cache := t.TempDir()
	options := FetchOptions{CacheDir: cache, Client: client, PublicKey: key.publicKeyLine()}
	if _, err := LoadIndex(context.Background(), url, options); err != nil {
		t.Fatal(err)
	}
	// Rewrite the cached index in place, leaving its signature untouched.
	cached, err := filepath.Glob(filepath.Join(cache, "*", "index.json"))
	if err != nil || len(cached) != 1 {
		t.Fatalf("expected one cached index, got %v (%v)", cached, err)
	}
	poisoned := Index{SchemaVersion: 1, Plugins: []RegistryPlugin{*testEntry(testRelease("9.9.9", ">= 0.1.0-0"))}}
	encoded, err := json.Marshal(poisoned)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached[0], encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	options.Offline = true
	if _, err := LoadIndex(context.Background(), url, options); err == nil || !strings.Contains(err.Error(), "failed signature verification") {
		t.Fatalf("a poisoned cache must be caught, got %v", err)
	}
}

func TestLoadIndexReportsAnUnsignedRegistry(t *testing.T) {
	url, client := signedRegistry(t, newSigningKey(t), false, false)
	loaded, err := LoadIndex(context.Background(), url, FetchOptions{CacheDir: t.TempDir(), Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Unsigned {
		t.Fatal("without a pinned key the index is unverified and must say so")
	}
}

func TestLoadIndexRejectsAMalformedPinnedKey(t *testing.T) {
	url, client := signedRegistry(t, newSigningKey(t), false, true)
	_, err := LoadIndex(context.Background(), url, FetchOptions{CacheDir: t.TempDir(), Client: client, PublicKey: "nonsense"})
	if err == nil || !strings.Contains(err.Error(), "pinned registry public key") {
		t.Fatalf("a bad key is a configuration error, got %v", err)
	}
}

// A file:// mirror carries the upstream signature verbatim and verifies exactly
// like the original.
func TestLoadIndexVerifiesAFileMirror(t *testing.T) {
	key := newSigningKey(t)
	dir := t.TempDir()
	body := indexJSON(t)
	path := filepath.Join(dir, "index.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+signatureSuffix, key.sign(body, minisignAlgorithmLegacy, "mirror"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIndex(context.Background(), "file://"+path, FetchOptions{PublicKey: key.publicKeyLine()})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Unsigned {
		t.Fatal("a verified mirror must not be reported as unsigned")
	}
	if err := os.Remove(path + signatureSuffix); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIndex(context.Background(), "file://"+path, FetchOptions{PublicKey: key.publicKeyLine()}); err == nil {
		t.Fatal("a pinned mirror without a signature must be refused")
	}
}
