package prometheus

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

// createSilence posts a silence to POST /api/v2/silences and returns the
// silence ID Alertmanager assigned. Callers build the matchers from the exact
// label set of the alert being silenced, so the silence suppresses that one
// alert and nothing else.
//
// A retried POST can in principle create a second identical silence (if the
// first attempt succeeded but its response was lost). Two silences with the
// same matchers and window are harmless — they suppress the same alert for the
// same period — and the adapter records the first returned ID so one
// Acknowledge never silences twice.
func (c *client) createSilence(ctx context.Context, s silence) (string, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("prometheus: encoding silence: %w", err)
	}

	data, err := c.do(ctx, http.MethodPost, "/api/v2/silences", nil, payload)
	if err != nil {
		return "", err
	}
	var resp silenceResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("prometheus: decoding silence response: %w", err)
	}
	return resp.SilenceID, nil
}

// alertGroups fetches the currently visible alerts already grouped by
// Alertmanager's routing tree from GET /api/v2/alerts/groups. Same visibility
// rules as alerts(): only active, unsilenced, uninhibited members.
func (c *client) alertGroups(ctx context.Context, filters []string) ([]alertGroup, error) {
	params := url.Values{
		"active":    {"true"},
		"silenced":  {"false"},
		"inhibited": {"false"},
	}
	for _, f := range filters {
		params.Add("filter", f)
	}

	data, err := c.get(ctx, "/api/v2/alerts/groups", params)
	if err != nil {
		return nil, err
	}
	var groups []alertGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, fmt.Errorf("prometheus: decoding alert groups response: %w", err)
	}
	return groups, nil
}

// allAlertGroups is alertGroups with suppressed members included, for the
// resolution check — a silenced group is still firing.
func (c *client) allAlertGroups(ctx context.Context) ([]alertGroup, error) {
	params := url.Values{
		"active":    {"true"},
		"silenced":  {"true"},
		"inhibited": {"true"},
	}

	data, err := c.get(ctx, "/api/v2/alerts/groups", params)
	if err != nil {
		return nil, err
	}
	var groups []alertGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, fmt.Errorf("prometheus: decoding alert groups response: %w", err)
	}
	return groups, nil
}

// allAlerts fetches every alert Alertmanager currently holds, including the
// silenced and inhibited ones the normal poll deliberately excludes. Resolution
// checks need this wider view: a silenced alert (notably one Apiary silenced
// itself via ack_via_silence) is still firing, and treating it as resolved
// would interrupt the very investigation that silenced it.
func (c *client) allAlerts(ctx context.Context) ([]alert, error) {
	params := url.Values{
		"active":      {"true"},
		"silenced":    {"true"},
		"inhibited":   {"true"},
		"unprocessed": {"true"},
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

// get executes a GET, retrying on 429/5xx with exponential backoff
// (honouring Retry-After when present).
func (c *client) get(ctx context.Context, path string, params url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, params, nil)
}

// do executes the request, retrying on 429/5xx with exponential backoff
// (honouring Retry-After when present). A nil body sends no request payload.
func (c *client) do(ctx context.Context, method, path string, params url.Values, body []byte) ([]byte, error) {
	fullPath := path
	if len(params) > 0 {
		fullPath += "?" + params.Encode()
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		var payload io.Reader
		if body != nil {
			payload = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+fullPath, payload)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearerToken)
		} else if c.basicUser != "" {
			req.SetBasicAuth(c.basicUser, c.basicPass)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			aplog.Debug("prometheus: %s %s failed (attempt %d/%d): %v", method, path, attempt+1, maxRetries, err)
			if !backoffWait(ctx, nil, attempt) {
				return nil, ctx.Err()
			}
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		aplog.Debug("prometheus: %s %s → %d", method, path, resp.StatusCode)

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("alertmanager API %s %s: status %d: %s", method, path, resp.StatusCode, truncateBody(respBody))
			if !backoffWait(ctx, resp, attempt) {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("alertmanager API %s %s: status %d: %s", method, path, resp.StatusCode, truncateBody(respBody))
		}
		return respBody, nil
	}
	return nil, fmt.Errorf("alertmanager API %s %s: exceeded %d retries: %w", method, path, maxRetries, lastErr)
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
