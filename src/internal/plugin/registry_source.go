package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Registry access is CLI-only: the daemon never reaches out to a registry, so
// nothing here runs in the dispatch path. Only https:// and file:// are
// accepted — digests protect the payload, but nothing protects a plaintext
// resolution, and pointing a client at an attacker-chosen index is the whole
// game.
const (
	registryFetchTimeout = 30 * time.Second
	// A registry index is metadata. Anything this size is a bug or an attack.
	maxIndexBytes = 8 << 20
	// Signatures live beside the index they cover, minisign's own convention.
	signatureSuffix = ".minisig"
)

// FetchOptions controls how an index is obtained.
type FetchOptions struct {
	// PublicKey is the minisign key this registry is pinned to. When set, the
	// index MUST carry a signature that verifies against it: a missing,
	// malformed, or mismatched signature is an error, and there is no flag to
	// proceed anyway. When empty, the index is used unverified and LoadedIndex
	// says so.
	PublicKey string
	// Offline uses the cache and never touches the network. A cold cache is an
	// error, not an empty result.
	Offline bool
	// CacheDir overrides the per-user cache location (tests, air-gapped setups).
	CacheDir string
	Client   *http.Client
	Timeout  time.Duration
}

// LoadedIndex is an index plus how it was obtained, so commands can be honest
// about serving cached data.
type LoadedIndex struct {
	*Index
	URL       string
	FromCache bool
	// Warning is set when the network failed but a usable cache existed. The
	// command succeeds; the operator is told the data may be stale.
	Warning error
	// Unsigned records that no key was available to verify this index against.
	// It is not a failure — it is the state of a registry whose signing key the
	// operator has not pinned — but commands surface it rather than implying a
	// check that did not happen.
	Unsigned bool
}

// ValidateRegistryURL accepts the two schemes a registry may be served over.
func ValidateRegistryURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("registry URL must not be empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("registry URL %q is not a URL: %w", raw, err)
	}
	switch parsed.Scheme {
	case "https", "file":
		return nil
	case "http":
		return fmt.Errorf("registry URL %q must use https: digests protect the download, nothing protects a plaintext index", raw)
	default:
		return fmt.Errorf("registry URL %q must use https:// or file://", raw)
	}
}

// LoadIndex fetches and parses one registry index, using a conditional GET
// against the local cache. A file:// index is read directly and never cached —
// it is already local, and caching it would only add a way to serve stale data.
func LoadIndex(ctx context.Context, rawURL string, opts FetchOptions) (*LoadedIndex, error) {
	if err := ValidateRegistryURL(rawURL); err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(rawURL)
	key, err := fetchKey(opts.PublicKey)
	if err != nil {
		return nil, err
	}

	if parsed.Scheme == "file" {
		path := filepath.FromSlash(parsed.Path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read registry index %q: %w", rawURL, err)
		}
		if key != nil {
			rawSignature, err := os.ReadFile(path + signatureSuffix)
			if err != nil {
				return nil, unsignedError(rawURL, key, err)
			}
			if err := VerifySignature(key, data, rawSignature); err != nil {
				return nil, fmt.Errorf("registry %q failed signature verification: %w", rawURL, err)
			}
		}
		index, err := ParseIndex(data)
		if err != nil {
			return nil, fmt.Errorf("registry %q: %w", rawURL, err)
		}
		return &LoadedIndex{Index: index, URL: rawURL, Unsigned: key == nil}, nil
	}

	cache := newIndexCache(opts.CacheDir, rawURL)
	if opts.Offline {
		data, _, err := cache.read()
		if err != nil {
			return nil, fmt.Errorf("--offline: no cached copy of %q; run once without --offline first", rawURL)
		}
		index, err := verifiedIndex(rawURL, data, cache.readSignature(), key)
		if err != nil {
			return nil, err
		}
		return &LoadedIndex{Index: index, URL: rawURL, FromCache: true, Unsigned: key == nil}, nil
	}

	cached, etag, cacheErr := cache.read()
	fetched, fetchErr := fetchIndex(ctx, rawURL, etag, opts)
	switch {
	case fetchErr != nil && cacheErr == nil:
		// The network is not the authority on whether we can work: a warm cache
		// still answers, as long as the operator is told it is a cache. It is
		// re-verified here, so a cache poisoned on disk is caught too.
		index, err := verifiedIndex(rawURL, cached, cache.readSignature(), key)
		if err != nil {
			return nil, fmt.Errorf("registry %q unreachable (%v) and %w", rawURL, fetchErr, err)
		}
		return &LoadedIndex{Index: index, URL: rawURL, FromCache: true, Warning: fetchErr, Unsigned: key == nil}, nil
	case fetchErr != nil:
		return nil, fetchErr
	case fetched.notModified:
		index, err := verifiedIndex(rawURL, cached, cache.readSignature(), key)
		if err != nil {
			return nil, err
		}
		return &LoadedIndex{Index: index, URL: rawURL, FromCache: true, Unsigned: key == nil}, nil
	}

	var rawSignature []byte
	if key != nil {
		signature, signatureErr := fetchIndex(ctx, rawURL+signatureSuffix, "", opts)
		if signatureErr != nil {
			return nil, unsignedError(rawURL, key, signatureErr)
		}
		rawSignature = signature.data
	}
	index, err := verifiedIndex(rawURL, fetched.data, rawSignature, key)
	if err != nil {
		// A response that does not verify or does not parse must not poison the
		// cache: the next run should get a clean fetch rather than inherit this.
		return nil, err
	}
	cache.write(fetched.data, fetched.etag)
	cache.writeSignature(rawSignature)
	return &LoadedIndex{Index: index, URL: rawURL, Unsigned: key == nil}, nil
}

// verifiedIndex checks the signature (when a key is pinned) before the bytes are
// interpreted at all, then parses.
func verifiedIndex(rawURL string, data, rawSignature []byte, key *PublicKey) (*Index, error) {
	if key != nil {
		if len(rawSignature) == 0 {
			return nil, unsignedError(rawURL, key, errors.New("no signature alongside the index"))
		}
		if err := VerifySignature(key, data, rawSignature); err != nil {
			return nil, fmt.Errorf("registry %q failed signature verification: %w", rawURL, err)
		}
	}
	index, err := ParseIndex(data)
	if err != nil {
		return nil, fmt.Errorf("registry %q: %w", rawURL, err)
	}
	return index, nil
}

// fetchKey parses the pinned key once, up front: a malformed key in config is a
// configuration error, not a verification failure to be discovered later.
func fetchKey(raw string) (*PublicKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	key, err := ParsePublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("pinned registry public key: %w", err)
	}
	return key, nil
}

func unsignedError(rawURL string, key *PublicKey, cause error) error {
	return fmt.Errorf("registry %q is pinned to signing key %s but no valid signature could be read from %s%s (%v); refusing to use an unverified index",
		rawURL, key.ID(), rawURL, signatureSuffix, cause)
}

// fetchResult keeps the ETag with the body it belongs to, so caching stays an
// implementation detail of this file rather than shared state.
type fetchResult struct {
	data        []byte
	etag        string
	notModified bool
}

func fetchIndex(ctx context.Context, rawURL, etag string, opts FetchOptions) (fetchResult, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = registryFetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchResult{}, fmt.Errorf("registry %q: %w", rawURL, err)
	}
	request.Header.Set("Accept", "application/json")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return fetchResult{}, fmt.Errorf("fetch registry %q: %w", rawURL, err)
	}
	defer func() { _ = response.Body.Close() }()

	switch response.StatusCode {
	case http.StatusNotModified:
		return fetchResult{notModified: true}, nil
	case http.StatusOK:
	default:
		return fetchResult{}, fmt.Errorf("fetch registry %q: unexpected status %s", rawURL, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxIndexBytes+1))
	if err != nil {
		return fetchResult{}, fmt.Errorf("read registry %q: %w", rawURL, err)
	}
	if len(body) > maxIndexBytes {
		return fetchResult{}, fmt.Errorf("registry %q is larger than %d bytes; refusing it", rawURL, maxIndexBytes)
	}
	return fetchResult{data: body, etag: response.Header.Get("ETag")}, nil
}

// indexCache stores one index per registry URL, keyed by a digest of the URL so
// two registries never collide and no path component is attacker-controlled.
type indexCache struct{ dir string }

func newIndexCache(override, rawURL string) *indexCache {
	base := override
	if base == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return &indexCache{}
		}
		base = filepath.Join(userCache, "apiary", "registry")
	}
	sum := sha256.Sum256([]byte(rawURL))
	return &indexCache{dir: filepath.Join(base, hex.EncodeToString(sum[:])[:16])}
}

// readSignature returns the cached signature, or nil. A missing one is not an
// error here: whether that is fatal depends on whether a key is pinned, and
// that decision belongs to the caller.
func (c *indexCache) readSignature() []byte {
	if c.dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(c.dir, "index.json"+signatureSuffix))
	if err != nil {
		return nil
	}
	return data
}

func (c *indexCache) writeSignature(data []byte) {
	if c.dir == "" || len(data) == 0 {
		return
	}
	_ = os.WriteFile(filepath.Join(c.dir, "index.json"+signatureSuffix), data, 0o644)
}

func (c *indexCache) read() ([]byte, string, error) {
	if c.dir == "" {
		return nil, "", errors.New("no cache directory")
	}
	data, err := os.ReadFile(filepath.Join(c.dir, "index.json"))
	if err != nil {
		return nil, "", err
	}
	etag, _ := os.ReadFile(filepath.Join(c.dir, "etag"))
	return data, strings.TrimSpace(string(etag)), nil
}

// write is best-effort: a registry command must not fail because a cache could
// not be written.
func (c *indexCache) write(data []byte, etag string) {
	if c.dir == "" {
		return
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	temp, err := os.CreateTemp(c.dir, ".index-*")
	if err != nil {
		return
	}
	name := temp.Name()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, filepath.Join(c.dir, "index.json")); err != nil {
		_ = os.Remove(name)
		return
	}
	_ = os.WriteFile(filepath.Join(c.dir, "etag"), []byte(etag), 0o644)
}
