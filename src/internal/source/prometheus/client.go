package prometheus

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
)

type client struct {
	baseURL     string
	bearerToken string
	basicUser   string
	basicPass   string
	httpClient  *http.Client
}

func newClient(baseURL, bearerToken, basicUser, basicPass string) *client {
	return &client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		bearerToken: bearerToken,
		basicUser:   basicUser,
		basicPass:   basicPass,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

// alerts fetches the currently visible alerts from GET /api/v2/alerts.
// filters are Alertmanager matcher strings (e.g. `severity="critical"`),
// passed as repeated filter= params; Alertmanager ANDs them.
func (c *client) alerts(ctx context.Context, filters []string) ([]alert, error) {
	params := url.Values{
		"active":      {"true"},
		"silenced":    {"false"},
		"inhibited":   {"false"},
		"unprocessed": {"false"},
	}
	for _, f := range filters {
		params.Add("filter", f)
	}

	data, err := c.get(ctx, "/api/v2/alerts", params)
	if err != nil {
		return nil, err
	}
	var alerts []alert
	if err := json.Unmarshal(data, &alerts); err != nil {
		return nil, fmt.Errorf("prometheus: decoding alerts response: %w", err)
	}
	return alerts, nil
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
		if c.bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearerToken)
		} else if c.basicUser != "" {
			req.SetBasicAuth(c.basicUser, c.basicPass)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			aplog.Debug("prometheus: GET %s failed (attempt %d/%d): %v", path, attempt+1, maxRetries, err)
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

		aplog.Debug("prometheus: GET %s → %d", path, resp.StatusCode)

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("alertmanager API GET %s: status %d: %s", path, resp.StatusCode, truncateBody(body))
			if !backoffWait(ctx, resp, attempt) {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("alertmanager API GET %s: status %d: %s", path, resp.StatusCode, truncateBody(body))
		}
		return body, nil
	}
	return nil, fmt.Errorf("alertmanager API GET %s: exceeded %d retries: %w", path, maxRetries, lastErr)
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
