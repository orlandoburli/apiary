package dynatrace

import (
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
)

const (
	maxRetries  = 3
	baseBackoff = time.Second

	// pageSize is the maximum the problems API allows per page.
	pageSize = 500
)

type client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

func newClient(baseURL, apiToken string) *client {
	return &client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// problems fetches every problem matching the selector from
// GET /api/v2/problems, following nextPageKey pagination. from bounds the
// problem start time (the API defaults to now-2h, which would hide older
// still-open problems).
func (c *client) problems(ctx context.Context, selector string, from time.Time) ([]problem, error) {
	params := url.Values{
		"pageSize": {strconv.Itoa(pageSize)},
		"from":     {from.UTC().Format(time.RFC3339)},
	}
	if selector != "" {
		params.Set("problemSelector", selector)
	}

	var all []problem
	for {
		data, err := c.get(ctx, "/api/v2/problems", params)
		if err != nil {
			return nil, err
		}
		var page problemsPage
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("dynatrace: decoding problems response: %w", err)
		}
		all = append(all, page.Problems...)
		if page.NextPageKey == "" {
			return all, nil
		}
		// Subsequent pages take ONLY nextPageKey; other params are rejected.
		params = url.Values{"nextPageKey": {page.NextPageKey}}
	}
}

// get executes the request, retrying on 429/5xx with exponential backoff
// (honouring Retry-After when present).
func (c *client) get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	fullPath := path
	if len(params) > 0 {
		fullPath += "?" + params.Encode()
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+fullPath, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Api-Token "+c.apiToken)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			aplog.Debug("dynatrace: GET %s failed (attempt %d/%d): %v", path, attempt+1, maxRetries, err)
			if !backoffWait(ctx, nil, attempt) {
				return nil, ctx.Err()
			}
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		aplog.Debug("dynatrace: GET %s → %d", path, resp.StatusCode)

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("dynatrace API GET %s: status %d: %s", path, resp.StatusCode, truncateBody(body))
			if !backoffWait(ctx, resp, attempt) {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("dynatrace API GET %s: status %d: %s", path, resp.StatusCode, truncateBody(body))
		}
		return body, nil
	}
	return nil, fmt.Errorf("dynatrace API GET %s: exceeded %d retries: %w", path, maxRetries, lastErr)
}

// backoffWait sleeps for the retry delay (Retry-After header if present,
// exponential backoff otherwise). Returns false when the context was cancelled.
func backoffWait(ctx context.Context, resp *http.Response, attempt int) bool {
	wait := baseBackoff * (1 << attempt)
	if resp != nil {
		if h := resp.Header.Get("Retry-After"); h != "" {
			if secs, err := strconv.Atoi(h); err == nil {
				wait = time.Duration(secs) * time.Second
			}
		}
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(wait):
		return true
	}
}

func truncateBody(b []byte) string {
	const max = 512
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
