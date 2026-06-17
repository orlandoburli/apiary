package codeberg

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

	aplog "github.com/orlandoburli/apiary/internal/log"
	"github.com/orlandoburli/apiary/internal/source"
)

// defaultBaseURL is Codeberg's API root. Any self-hosted Forgejo/Gitea instance
// works by overriding config.base_url (e.g. https://git.example.org/api/v1).
const defaultBaseURL = "https://codeberg.org/api/v1"

// perPage is Forgejo's max page size for list endpoints.
const perPage = 50

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
	token := c.apiKey
	if t, ok := ctx.Value(source.SourceTokenCtxKey).(string); ok && t != "" {
		token = t
	}
	// Forgejo/Gitea's canonical PAT scheme is "Authorization: token <TOKEN>"
	// (the literal word "token" is kept for historical reasons and is the most
	// universally supported form across versions).
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/json")
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
	aplog.Debug("codeberg: %s %s → %d", req.Method, req.URL, resp.StatusCode)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("codeberg API %s: status %d: %s", req.Method, resp.StatusCode, body)
	}
	return body, nil
}

func (c *client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
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

func (c *client) delete(ctx context.Context, path string) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, req)
}

// getAllIssues pages through a list endpoint following the RFC 5988 Link header
// (Forgejo sets rel="next" like GitHub). per_page is capped at Forgejo's max.
func (c *client) getAllIssues(ctx context.Context, path string, params url.Values) ([]issue, error) {
	var all []issue
	page := 1

	for {
		p := url.Values{}
		for k, v := range params {
			p[k] = v
		}
		p.Set("limit", strconv.Itoa(perPage))
		p.Set("page", strconv.Itoa(page))

		req, err := c.newRequest(ctx, http.MethodGet, path+"?"+p.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		aplog.Debug("codeberg: GET %s → %d", req.URL, resp.StatusCode)

		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("codeberg API: status %d: %s", resp.StatusCode, body)
		}

		var issues []issue
		if err := json.Unmarshal(body, &issues); err != nil {
			return nil, fmt.Errorf("codeberg: decoding issues: %w", err)
		}
		all = append(all, issues...)

		if !hasNextPage(resp.Header.Get("Link")) {
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
