package plane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const defaultBaseURL = "https://api.plane.so"

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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("plane API %s %s: status %d: %s", http.MethodGet, path, resp.StatusCode, body)
	}
	return body, nil
}

func (c *client) patch(ctx context.Context, path string, payload any) ([]byte, error) {
	return c.doJSON(ctx, http.MethodPatch, path, payload)
}

func (c *client) post(ctx context.Context, path string, payload any) ([]byte, error) {
	return c.doJSON(ctx, http.MethodPost, path, payload)
}

func (c *client) doJSON(ctx context.Context, method, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("plane API %s %s: status %d: %s", method, path, resp.StatusCode, body)
	}
	return body, nil
}

// getAll paginates through all pages of a work-items or states/labels endpoint.
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

		if pg.NextCursor == nil || *pg.NextCursor == "" {
			break
		}
		cursor = *pg.NextCursor
	}

	return all, nil
}
