package plane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
)

const (
	defaultBaseURL = "https://api.plane.so"
	maxRetries     = 5
	baseBackoff    = time.Second
)

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
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

func (c *client) get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return c.doWithRetry(ctx, http.MethodGet, u, nil)
}

// getNoLog performs a single GET without retry or response logging.
// Used for endpoint probes where a 404 is an expected outcome.
func (c *client) getNoLog(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return body, nil
}

func (c *client) patch(ctx context.Context, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.doWithRetry(ctx, http.MethodPatch, c.baseURL+path, data)
}

func (c *client) post(ctx context.Context, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return c.doWithRetry(ctx, http.MethodPost, c.baseURL+path, data)
}

// doWithRetry executes the request, retrying on 429 with exponential backoff.
// It honours the Retry-After header when present.
func (c *client) doWithRetry(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-API-Key", c.apiKey)
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

		aplog.Debug("plane: %s %s → %d", method, url, resp.StatusCode)

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := retryAfter(resp, attempt)
			lastErr = fmt.Errorf("rate limited")
			aplog.Info("plane: rate limited — waiting %s before retry %d/%d",
				wait.Round(time.Millisecond), attempt+1, maxRetries)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("plane API %s: status %d: %s", method, resp.StatusCode, respBody)
		}
		return respBody, nil
	}

	return nil, fmt.Errorf("plane API %s %s: exceeded %d retries: %w", method, url, maxRetries, lastErr)
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

// getAll paginates through all pages of an endpoint.
func getAll[T any](ctx context.Context, c *client, path string) ([]T, error) {
	var all []T
	cursor := ""

	for {
		params := url.Values{"per_page": {"100"}}
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		data, err := c.get(ctx, path, params)
		if err != nil {
			return nil, err
		}

		var pg page[T]
		if err := json.Unmarshal(data, &pg); err != nil {
			return nil, fmt.Errorf("plane: decoding page from %s: %w", path, err)
		}

		all = append(all, pg.Results...)

		if !pg.NextPageResults {
			break
		}
		cursor = pg.NextCursor
	}

	return all, nil
}
