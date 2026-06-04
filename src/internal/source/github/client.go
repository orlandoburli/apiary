package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	aplog "github.com/orlandoburli/apiary/internal/log"
)

const (
	defaultBaseURL = "https://api.github.com"
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
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

func (c *client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *client) do(ctx context.Context, req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	aplog.Debug("github: %s %s → %d", req.Method, req.URL, resp.StatusCode)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("github API %s: status %d: %s", req.Method, resp.StatusCode, body)
	}
	return body, nil
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

func (c *client) getAllIssues(ctx context.Context, path string, params url.Values) ([]issue, error) {
	var all []issue
	page := 1

	for {
		p := url.Values{}
		for k, v := range params {
			p[k] = v
		}
		p.Set("per_page", "100")
		p.Set("page", strconv.Itoa(page))

		u := c.baseURL + path + "?" + p.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		req.Header.Set("Accept", "application/vnd.github.v3+json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("github API: status %d: %s", resp.StatusCode, body)
		}

		var issues []issue
		if err := json.Unmarshal(body, &issues); err != nil {
			return nil, fmt.Errorf("github: decoding issues: %w", err)
		}

		all = append(all, issues...)

		link := resp.Header.Get("Link")
		if !hasNextPage(link) {
			break
		}
		page++
	}

	return all, nil
}

var linkNextRe = regexp.MustCompile(`<[^>]+>;\s*rel="([^"]+)"`)

func hasNextPage(link string) bool {
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		m := linkNextRe.FindStringSubmatch(part)
		if len(m) == 2 && m[1] == "next" {
			return true
		}
	}
	return false
}
