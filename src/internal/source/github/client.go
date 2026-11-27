package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/source"
)

const (
	defaultBaseURL = "https://api.github.com"

	// maxListPages bounds a single paginated read. A timeline can grow without
	// limit in a pathological repo, and following Link forever would let one
	// poll run unbounded; 20 pages × 100 events is far past any real issue.
	maxListPages = 20
)

// getRetryBackoff is how long to wait before each retry of a failed idempotent
// GET. Two attempts cover a reset GitHub connection (GOAWAY, idle reset) without
// holding a poll open: 250ms, then 1s.
var getRetryBackoff = []time.Duration{250 * time.Millisecond, time.Second}

// apiError is a response GitHub did return, including an error status. It is
// kept distinct from transport failures because retrying it would replay the
// same answer: a 404 stays a 404 and a 403 still lacks the permission.
type apiError struct {
	method string
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("github API %s: status %d: %s", e.method, e.status, e.body)
}

type client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newClient(baseURL, apiKey string) *client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

func (c *client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	return c.newRawRequest(ctx, method, c.baseURL+path, body)
}

func (c *client) newRawRequest(ctx context.Context, method, rawURL string, body []byte) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return nil, err
	}
	token := c.apiKey
	if t, ok := ctx.Value(source.SourceTokenCtxKey).(string); ok && t != "" {
		token = t
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do performs a request, retrying an idempotent GET whose failure looks like a
// dropped connection rather than an answer from GitHub.
func (c *client) do(ctx context.Context, req *http.Request) ([]byte, error) {
	body, _, err := c.doWithHeader(ctx, req)
	return body, err
}

func (c *client) doWithHeader(ctx context.Context, req *http.Request) ([]byte, http.Header, error) {
	for attempt := 0; ; attempt++ {
		body, header, err := c.doOnce(req)
		if err == nil {
			return body, header, nil
		}
		if !retryableGet(ctx, req, err, attempt) {
			return nil, nil, err
		}
		timer := time.NewTimer(getRetryBackoff[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, err
		case <-timer.C:
		}
	}
}

func (c *client) doOnce(req *http.Request) ([]byte, http.Header, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	aplog.Debug("github: %s %s → %d", req.Method, req.URL, resp.StatusCode)

	// Check the status before the read error: whatever the body says, a 4xx/5xx
	// is GitHub's answer and retrying it would only repeat it.
	if resp.StatusCode >= 400 {
		msg := string(body)
		if readErr != nil {
			msg = fmt.Sprintf("%s (reading body: %v)", msg, readErr)
		}
		return nil, nil, &apiError{method: req.Method, status: resp.StatusCode, body: msg}
	}
	if readErr != nil {
		// A truncated body is a transport failure, not a decode failure. Keeping
		// the read error lets the GET retry and stops it surfacing as the
		// misleading "unexpected end of JSON input" (#558).
		return nil, nil, fmt.Errorf("github: reading %s %s response: %w", req.Method, req.URL, readErr)
	}
	return body, resp.Header, nil
}

// retryableGet decides whether a failed request may be safely replayed. Only
// GETs are idempotent here, and only transport-level failures are transient; an
// API status, an exhausted budget, or an already-cancelled context is final.
func retryableGet(ctx context.Context, req *http.Request, err error, attempt int) bool {
	if attempt >= len(getRetryBackoff) || req.Method != http.MethodGet || ctx.Err() != nil {
		return false
	}
	var api *apiError
	if errors.As(err, &api) {
		return false
	}
	return isTransientTransportError(err)
}

func isTransientTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := err.Error()
	for _, marker := range []string{
		"unexpected EOF",
		"http2: stream closed",
		"GOAWAY",
		"connection reset",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func (c *client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, req)
}

func (c *client) patch(ctx context.Context, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPatch, path, data)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, req)
}

func (c *client) post(ctx context.Context, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, data)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, req)
}

func (c *client) delete(ctx context.Context, path string) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, req)
}

// getAllIssues reads every page of an issue list endpoint. Its pagination is
// shared with the issue timeline so both follow GitHub's Link header.
func (c *client) getAllIssues(ctx context.Context, path string, params url.Values) ([]issue, error) {
	return fetchPages[issue](ctx, c, path, params)
}

// fetchPages reads a list endpoint page by page, following Link rel="next" and
// appending the decoded items. The page cap keeps a single poll bounded; when it
// trips we say so, because silently truncating is how a cross-reference that
// lives past page 20 would become another permanent "pending" (#557).
func fetchPages[T any](ctx context.Context, c *client, path string, params url.Values) ([]T, error) {
	p := url.Values{}
	for k, v := range params {
		p[k] = v
	}
	p.Set("per_page", "100")
	next := c.baseURL + path + "?" + p.Encode()

	var all []T
	for page := 1; next != ""; page++ {
		if page > maxListPages {
			aplog.Warn("github: %s: stopped after %d pages — more items exist but one poll must stay bounded", path, maxListPages)
			break
		}
		body, link, err := c.getPage(ctx, next)
		if err != nil {
			return nil, err
		}
		var items []T
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("github: decoding %s page %d: %w", path, page, err)
		}
		all = append(all, items...)
		next = nextLink(link)
	}
	return all, nil
}

// getPage GETs an absolute URL (the next link GitHub hands back) and returns the
// Link header as well.
func (c *client) getPage(ctx context.Context, rawURL string) ([]byte, string, error) {
	req, err := c.newRawRequest(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	body, header, err := c.doWithHeader(ctx, req)
	if err != nil {
		return nil, "", err
	}
	return body, header.Get("Link"), nil
}

// decodePages fetches every page of a list endpoint (per_page=100) and decodes
// the concatenation. It stops when a page comes back short — the standard
// heuristic that avoids threading Link headers through the client.
func decodePages[T any](ctx context.Context, c *client, path string, params url.Values) ([]T, error) {
	const perPage = 100
	var all []T
	for page := 1; ; page++ {
		p := url.Values{}
		for k, v := range params {
			p[k] = v
		}
		p.Set("per_page", strconv.Itoa(perPage))
		p.Set("page", strconv.Itoa(page))

		body, err := c.get(ctx, path+"?"+p.Encode())
		if err != nil {
			return nil, err
		}
		var items []T
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("github: decoding %s page %d: %w", path, page, err)
		}
		all = append(all, items...)
		if len(items) < perPage {
			return all, nil
		}
	}
}

var linkPartRe = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="([^"]+)"`)

// nextLink returns the rel="next" URL from a Link header, or "" when the page is
// the last one.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		m := linkPartRe.FindStringSubmatch(strings.TrimSpace(part))
		if len(m) == 3 && m[2] == "next" {
			return m[1]
		}
	}
	return ""
}
