package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/source"
)

const (
	maxRetries     = 5
	baseBackoff    = time.Second
	maxSearchPages = 50
)

type client struct {
	baseURL    string
	email      string
	apiToken   string
	httpClient *http.Client
}

func newClient(baseURL, email, apiToken string) *client {
	return &client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		email:      email,
		apiToken:   apiToken,
		httpClient: &http.Client{},
	}
}

// credentials returns the Basic-auth pair for a request. A per-agent override
// from source.SourceTokenCtxKey replaces the API token; when the override
// contains ":" it is treated as "email:api_token" and replaces both (Jira
// Cloud Basic auth always needs the account email alongside the token).
func (c *client) credentials(ctx context.Context) (email, token string) {
	email, token = c.email, c.apiToken
	if t, ok := ctx.Value(source.SourceTokenCtxKey).(string); ok && t != "" {
		if e, tok, found := strings.Cut(t, ":"); found {
			email, token = e, tok
		} else {
			token = t
		}
	}
	return email, token
}

func (c *client) get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return c.doWithRetry(ctx, http.MethodGet, path, nil)
}

func (c *client) post(ctx context.Context, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.doWithRetry(ctx, http.MethodPost, path, data)
}

func (c *client) put(ctx context.Context, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.doWithRetry(ctx, http.MethodPut, path, data)
}

// doWithRetry executes the request, retrying on 429 with exponential backoff.
// It honours the Retry-After header when present. Error responses include the
// body so Jira's errorMessages (e.g. JQL syntax errors) stay visible.
func (c *client) doWithRetry(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
		if err != nil {
			return nil, err
		}
		email, token := c.credentials(ctx)
		req.SetBasicAuth(email, token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		aplog.Debug("jira: %s %s → %d", method, req.URL.Path, resp.StatusCode)

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := retryAfter(resp, attempt)
			lastErr = fmt.Errorf("rate limited")
			aplog.Info("jira: rate limited — waiting %s before retry %d/%d",
				wait.Round(time.Millisecond), attempt+1, maxRetries)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("jira API %s %s: status %d: %s", method, req.URL.Path, resp.StatusCode, respBody)
		}
		return respBody, nil
	}

	return nil, fmt.Errorf("jira API %s %s: exceeded %d retries: %w", method, path, maxRetries, lastErr)
}

// retryAfter returns how long to wait before the next attempt.
// Uses the Retry-After header if present, otherwise exponential backoff.
func retryAfter(resp *http.Response, attempt int) time.Duration {
	if h := resp.Header.Get("Retry-After"); h != "" {
		if secs, err := strconv.Atoi(h); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	// exponential backoff: 1s, 2s, 4s, 8s, …
	return baseBackoff * (1 << attempt)
}

// searchFields is every issue field Poll/PollTask need; user-identifying
// fields (assignee, reporter) are deliberately not requested.
const searchFields = "summary,description,status,labels,priority,issuetype,created,updated"

// searchAll pages through GET /rest/api/3/search/jql. The legacy
// /rest/api/3/search endpoint was removed from Jira Cloud in 2025 and returns
// 410 Gone — do not "simplify" back to it. Pagination is an opaque
// nextPageToken; the loop is additionally guarded against a server that
// repeats a token or never stops handing them out.
func (c *client) searchAll(ctx context.Context, jql string) ([]issue, error) {
	var all []issue
	token := ""

	for page := 0; page < maxSearchPages; page++ {
		params := url.Values{
			"jql":        {jql},
			"maxResults": {"100"},
			"fields":     {searchFields},
		}
		if token != "" {
			params.Set("nextPageToken", token)
		}

		data, err := c.get(ctx, "/rest/api/3/search/jql", params)
		if err != nil {
			return nil, err
		}

		var pg searchResponse
		if err := json.Unmarshal(data, &pg); err != nil {
			return nil, fmt.Errorf("jira: decoding search page: %w", err)
		}

		all = append(all, pg.Issues...)

		if pg.NextPageToken == "" || pg.NextPageToken == token {
			return all, nil
		}
		token = pg.NextPageToken
	}

	return nil, fmt.Errorf("jira: search pagination exceeded %d pages — narrow filters.jql", maxSearchPages)
}
