// Package cursorusage fetches per-request usage events from Cursor's dashboard
// API and attributes their cost to apiary runs. The Cursor agent CLI does not
// report cost in its stream output (only token counts), so dollar amounts must
// be recovered after the fact from the same endpoint the cursor.com usage tab
// uses, authenticated with the user's WorkosCursorSessionToken browser cookie.
//
// The endpoint is undocumented and may change without notice; everything in
// this package is best-effort and must degrade to "no cost recorded" on any
// failure.
package cursorusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the Cursor dashboard host. Overridable for tests.
const DefaultBaseURL = "https://cursor.com"

// maxPages bounds pagination per fetch so a huge window cannot loop forever.
const maxPages = 20

const pageSize = 100

// Client calls Cursor's private dashboard API with cookie auth.
type Client struct {
	// Token is the WorkosCursorSessionToken cookie value, copied from a logged-in
	// cursor.com browser session (F12 → Application → Cookies). Lasts ~60 days.
	Token   string
	BaseURL string
	HTTP    *http.Client
	// TeamID scopes the query to a team account. Personal accounts use 0.
	// Accounts whose usage is billed through a team (the common per-usage
	// setup) MUST pass their team id or the API returns no events.
	TeamID int
	// UserID filters team queries to one member's events, so teammates'
	// activity does not pollute time-window attribution. Omitted when 0.
	UserID int
}

// TokenUsage is the per-event token breakdown. Field names mirror the API's
// protobuf-es camelCase JSON. TotalCents is the model cost in fractional cents
// and may be absent.
type TokenUsage struct {
	InputTokens      int     `json:"inputTokens"`
	OutputTokens     int     `json:"outputTokens"`
	CacheWriteTokens int     `json:"cacheWriteTokens"`
	CacheReadTokens  int     `json:"cacheReadTokens"`
	TotalCents       float64 `json:"totalCents"`
}

// UsageEvent is one model request as reported by the dashboard. There is no
// session or request id — correlation to a run is by timestamp window only.
type UsageEvent struct {
	// Timestamp is UTC epoch milliseconds serialized as a string.
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
	// Kind is a USAGE_EVENT_KIND_* enum name, e.g. USAGE_EVENT_KIND_USAGE_BASED
	// or USAGE_EVENT_KIND_ERRORED_NOT_CHARGED.
	Kind             string      `json:"kind"`
	MaxMode          bool        `json:"maxMode"`
	UsageBasedCosts  string      `json:"usageBasedCosts"`
	IsTokenBasedCall bool        `json:"isTokenBasedCall"`
	TokenUsage       *TokenUsage `json:"tokenUsage"`
	// CursorTokenFee is Cursor's markup in fractional cents.
	CursorTokenFee float64 `json:"cursorTokenFee"`
	IsChargeable   bool    `json:"isChargeable"`
	// IsHeadless marks Cursor's hosted background-agent product. Verified live:
	// cursor-agent CLI runs report false, same as interactive IDE usage, so this
	// CANNOT distinguish apiary runs from IDE activity and is kept for
	// observability only.
	IsHeadless *bool `json:"isHeadless"`
	// ChargedCents is the amount actually billed in fractional cents
	// (≈ TokenUsage.TotalCents + CursorTokenFee). Newer schema; may be absent.
	ChargedCents float64 `json:"chargedCents"`
}

// Time parses the event's epoch-millisecond timestamp. Zero time on garbage.
func (e UsageEvent) Time() time.Time {
	ms, err := strconv.ParseInt(e.Timestamp, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// CostUSD returns the event's billed cost in dollars, preferring the newer
// chargedCents field, then totalCents + markup, then the "$x.xx" display
// string. Not-charged events return 0 regardless of any stale cost fields.
func (e UsageEvent) CostUSD() float64 {
	if strings.HasSuffix(e.Kind, "_NOT_CHARGED") {
		return 0
	}
	if e.ChargedCents > 0 {
		return e.ChargedCents / 100
	}
	if e.TokenUsage != nil && e.TokenUsage.TotalCents > 0 {
		return (e.TokenUsage.TotalCents + e.CursorTokenFee) / 100
	}
	if s := strings.TrimPrefix(e.UsageBasedCosts, "$"); s != e.UsageBasedCosts {
		if v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64); err == nil && v > 0 {
			return v
		}
	}
	return 0
}

type eventsRequest struct {
	TeamID    int    `json:"teamId"`
	UserID    int    `json:"userId,omitempty"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Page      int    `json:"page"`
	PageSize  int    `json:"pageSize"`
}

type eventsResponse struct {
	TotalUsageEventsCount int          `json:"totalUsageEventsCount"`
	UsageEventsDisplay    []UsageEvent `json:"usageEventsDisplay"`
}

// FetchEvents returns all usage events with timestamps in [start, end],
// walking pages newest-first until the window is exhausted.
func (c *Client) FetchEvents(ctx context.Context, start, end time.Time) ([]UsageEvent, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}

	var all []UsageEvent
	for page := 1; page <= maxPages; page++ {
		body, err := json.Marshal(eventsRequest{
			TeamID:    c.TeamID,
			UserID:    c.UserID,
			StartDate: strconv.FormatInt(start.UnixMilli(), 10),
			EndDate:   strconv.FormatInt(end.UnixMilli(), 10),
			Page:      page,
			PageSize:  pageSize,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			base+"/api/dashboard/get-filtered-usage-events", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		// Without a cursor.com Origin the API rejects the call with
		// "Invalid origin for state-changing request".
		req.Header.Set("Origin", "https://cursor.com")
		req.Header.Set("Referer", "https://cursor.com/dashboard?tab=usage")
		req.AddCookie(&http.Cookie{Name: "WorkosCursorSessionToken", Value: c.Token})

		resp, err := httpc.Do(req)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("cursor dashboard auth failed (%d): session token expired or invalid", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("cursor dashboard returned %d: %s", resp.StatusCode, truncate(string(data), 200))
		}
		var er eventsResponse
		if err := json.Unmarshal(data, &er); err != nil {
			return nil, fmt.Errorf("cursor dashboard response: %w", err)
		}
		all = append(all, er.UsageEventsDisplay...)
		if len(er.UsageEventsDisplay) < pageSize {
			break
		}
	}
	return all, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
