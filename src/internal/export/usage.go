// Package export turns Apiary's execution history into files a spreadsheet can
// open. It reads through whatever *sql.DB it is handed — normally a read-only
// connection opened without migrations — so it is safe against a live daemon
// and tolerant of databases written by an older binary: a column the database
// does not have is exported as empty rather than failing the query.
package export

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Querier is the slice of *sql.DB this package needs.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Kind is the exported type of a column. Writers format by kind, so every
// column of one kind renders identically in CSV and JSON.
type Kind int

const (
	KindString Kind = iota
	KindInt
	KindFloat
	KindBool
	KindTime
)

// Column is one entry of the export contract: the exported name, the physical
// column it comes from, its kind, and whether it is opt-in.
type Column struct {
	Name string
	// table and column are the physical source, probed at query time. When the
	// database predates the column, the export selects NULL under the same name.
	table, column string
	Kind          Kind
	// Opt names the flag that enables the column; empty means always exported.
	Opt string
}

const (
	// OptTranscripts enables input_prompt and output_text.
	OptTranscripts = "transcripts"
	// OptSlowTools enables slow_tools.
	OptSlowTools = "slow_tools"
)

const (
	tableExecutions = "task_executions"
	tableInstances  = "workflow_instances"
)

// UsageColumns is the export contract for `apiary export usage`, in output
// order. Appending a column is a minor change; removing or reordering one is
// breaking. TestUsageColumnsCoverSchema fails when task_executions grows a
// column that is neither here nor in the explicit unexported list.
var UsageColumns = []Column{
	{Name: "execution_id", table: tableExecutions, column: "id", Kind: KindInt},
	{Name: "task_id", table: tableExecutions, column: "task_id", Kind: KindString},
	{Name: "task_number", table: tableExecutions, column: "task_number", Kind: KindString},
	{Name: "title", table: tableExecutions, column: "title", Kind: KindString},
	{Name: "task_url", table: tableExecutions, column: "task_url", Kind: KindString},
	{Name: "source_id", table: tableInstances, column: "source_id", Kind: KindString},
	{Name: "workflow_id", table: tableInstances, column: "workflow_id", Kind: KindString},
	{Name: "workflow_instance_id", table: tableExecutions, column: "workflow_instance_id", Kind: KindString},
	{Name: "instance_state", table: tableInstances, column: "state", Kind: KindString},
	{Name: "step_id", table: tableExecutions, column: "step_id", Kind: KindString},
	{Name: "agent_id", table: tableExecutions, column: "agent_id", Kind: KindString},
	{Name: "runner", table: tableExecutions, column: "runner", Kind: KindString},
	{Name: "model", table: tableExecutions, column: "model", Kind: KindString},
	{Name: "attempt", table: tableExecutions, column: "attempt", Kind: KindInt},
	{Name: "status", table: tableExecutions, column: "status", Kind: KindString},
	{Name: "failure_kind", table: tableExecutions, column: "failure_kind", Kind: KindString},
	{Name: "credit_exhausted", table: tableExecutions, column: "credit_exhausted", Kind: KindBool},
	{Name: "started_at", table: tableExecutions, column: "started_at", Kind: KindTime},
	{Name: "completed_at", table: tableExecutions, column: "completed_at", Kind: KindTime},
	{Name: "duration_ms", table: tableExecutions, column: "duration_ms", Kind: KindInt},
	{Name: "input_tokens", table: tableExecutions, column: "input_tokens", Kind: KindInt},
	{Name: "output_tokens", table: tableExecutions, column: "output_tokens", Kind: KindInt},
	{Name: "cache_creation_tokens", table: tableExecutions, column: "cache_creation_tokens", Kind: KindInt},
	{Name: "cache_read_tokens", table: tableExecutions, column: "cache_read_tokens", Kind: KindInt},
	{Name: "total_tokens", table: tableExecutions, column: "total_tokens", Kind: KindInt},
	{Name: "num_turns", table: tableExecutions, column: "num_turns", Kind: KindInt},
	{Name: "num_tool_calls", table: tableExecutions, column: "num_tool_calls", Kind: KindInt},
	{Name: "cost_usd", table: tableExecutions, column: "cost_usd", Kind: KindFloat},
	{Name: "time_thinking_ms", table: tableExecutions, column: "time_thinking_ms", Kind: KindInt},
	{Name: "time_writing_ms", table: tableExecutions, column: "time_writing_ms", Kind: KindInt},
	{Name: "time_model_ms", table: tableExecutions, column: "time_model_ms", Kind: KindInt},
	{Name: "time_tool_wait_ms", table: tableExecutions, column: "time_tool_wait_ms", Kind: KindInt},
	{Name: "time_other_ms", table: tableExecutions, column: "time_other_ms", Kind: KindInt},
	{Name: "time_background_ms", table: tableExecutions, column: "time_background_ms", Kind: KindInt},
	{Name: "error_message", table: tableExecutions, column: "error_message", Kind: KindString},
	{Name: "slow_tools", table: tableExecutions, column: "slow_tools", Kind: KindString, Opt: OptSlowTools},
	{Name: "input_prompt", table: tableExecutions, column: "input_prompt", Kind: KindString, Opt: OptTranscripts},
	{Name: "output_text", table: tableExecutions, column: "output_text", Kind: KindString, Opt: OptTranscripts},
}

// UnexportedExecutionColumns lists task_executions columns deliberately left
// out of the export: operational bookkeeping with no analytical value. A column
// in neither list fails TestUsageColumnsCoverSchema, so adding one to the
// schema forces a decision here.
var UnexportedExecutionColumns = []string{
	"pid", "heartbeat_at", "heartbeat_count", "can_retry", "next_retry_at", "created_at",
}

// UsageFilter narrows the export. Zero values mean "no constraint".
type UsageFilter struct {
	// Since and Until bound started_at: [Since, Until). Zero is unbounded.
	Since, Until time.Time
	Workflows    []string
	Agents       []string
	Models       []string
	Sources      []string
	// Statuses filters task_executions.status. Rows that never started
	// (NULL started_at) are excluded from the window unless "pending" is
	// listed here explicitly.
	Statuses           []string
	IncludeTranscripts bool
	IncludeSlowTools   bool
}

func (f UsageFilter) includePending() bool {
	return slices.Contains(f.Statuses, "pending")
}

// Columns returns the columns the filter selects, in export order.
func (f UsageFilter) Columns() []Column {
	out := make([]Column, 0, len(UsageColumns))
	for _, c := range UsageColumns {
		switch c.Opt {
		case "":
		case OptTranscripts:
			if !f.IncludeTranscripts {
				continue
			}
		case OptSlowTools:
			if !f.IncludeSlowTools {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// Row is one exported record, aligned with the filter's Columns(). Values are
// nil, string, int64, float64, bool or time.Time (UTC). A timestamp the
// database holds in a form this package cannot parse is passed through as the
// raw string so the row is not lost; the writer renders it verbatim.
type Row []any

// ListUsageRows streams every execution matching the filter, oldest first, to
// fn. It never buffers the result set: a busy hive has hundreds of thousands
// of executions and the transcript columns alone run to gigabytes.
func ListUsageRows(ctx context.Context, db Querier, f UsageFilter, fn func(Row) error) error {
	cols := f.Columns()
	present, err := probeColumns(ctx, db, tableExecutions, tableInstances)
	if err != nil {
		return err
	}

	selects := make([]string, len(cols))
	for i, c := range cols {
		if present[c.table][c.column] {
			selects[i] = tableAlias(c.table) + "." + c.column
		} else {
			selects[i] = "NULL"
		}
	}

	// A database from before the workflow engine has no instance link at all;
	// join on a constant so the wi.* selects still resolve (to NULL).
	join := "wi.id = te.workflow_instance_id"
	if !present[tableExecutions]["workflow_instance_id"] {
		join = "0"
	}

	where, args := usageWhere(f)
	// Never-started rows (NULL started_at, only with --status pending) go last
	// so the file reads chronologically.
	query := `SELECT ` + strings.Join(selects, ", ") + `
		FROM task_executions te
		LEFT JOIN workflow_instances wi ON ` + join + `
		` + where + `
		ORDER BY te.started_at IS NULL, te.started_at ASC, te.id ASC`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query usage: %w", err)
	}
	defer rows.Close()

	dests := make([]any, len(cols))
	for rows.Next() {
		for i, c := range cols {
			dests[i] = newDest(c.Kind)
		}
		if err := rows.Scan(dests...); err != nil {
			return fmt.Errorf("scan usage row: %w", err)
		}
		row := make(Row, len(cols))
		for i, c := range cols {
			row[i] = fromDest(c.Kind, dests[i])
		}
		if err := fn(row); err != nil {
			return err
		}
	}
	return rows.Err()
}

func tableAlias(table string) string {
	if table == tableInstances {
		return "wi"
	}
	return "te"
}

// usageWhere compiles the filter into a WHERE clause with bound parameters.
func usageWhere(f UsageFilter) (string, []any) {
	var clauses []string
	var args []any

	var window []string
	if !f.Since.IsZero() {
		window = append(window, "te.started_at >= ?")
		args = append(args, f.Since.UTC())
	}
	if !f.Until.IsZero() {
		window = append(window, "te.started_at < ?")
		args = append(args, f.Until.UTC())
	}
	switch {
	case f.includePending() && len(window) > 0:
		clauses = append(clauses, "(te.started_at IS NULL OR ("+strings.Join(window, " AND ")+"))")
	case f.includePending():
		// no window, pending allowed: nothing to constrain
	case len(window) > 0:
		clauses = append(clauses, "te.started_at IS NOT NULL", strings.Join(window, " AND "))
	default:
		clauses = append(clauses, "te.started_at IS NOT NULL")
	}

	in := func(expr string, vals []string) {
		if len(vals) == 0 {
			return
		}
		clauses = append(clauses, expr+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("wi.workflow_id", f.Workflows)
	in("te.agent_id", f.Agents)
	in("te.model", f.Models)
	in("wi.source_id", f.Sources)
	in("te.status", f.Statuses)

	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// probeColumns returns table → column → present for the given tables. The
// export never assumes a column exists: the connection is read-only, so it
// cannot migrate an older database up to the current schema.
func probeColumns(ctx context.Context, db Querier, tables ...string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	for _, table := range tables {
		rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			return nil, fmt.Errorf("probe %s: %w", table, err)
		}
		cols := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return nil, fmt.Errorf("probe %s: %w", table, err)
			}
			cols[name] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		out[table] = cols
	}
	return out, nil
}

func newDest(k Kind) any {
	switch k {
	case KindInt, KindBool:
		return new(sql.NullInt64)
	case KindFloat:
		return new(sql.NullFloat64)
	case KindTime:
		return new(any)
	default:
		return new(sql.NullString)
	}
}

func fromDest(k Kind, d any) any {
	switch k {
	case KindInt:
		v := d.(*sql.NullInt64)
		if !v.Valid {
			return nil
		}
		return v.Int64
	case KindBool:
		v := d.(*sql.NullInt64)
		if !v.Valid {
			return nil
		}
		return v.Int64 != 0
	case KindFloat:
		v := d.(*sql.NullFloat64)
		if !v.Valid {
			return nil
		}
		return v.Float64
	case KindTime:
		return normalizeTime(*d.(*any))
	default:
		v := d.(*sql.NullString)
		if !v.Valid {
			return nil
		}
		return v.String
	}
}

// timeLayouts are the forms Apiary has written timestamps in over its life:
// modernc's sqlite time format (current), RFC3339 (early rows), and bare
// SQLite datetime() output (CURRENT_TIMESTAMP defaults).
var timeLayouts = []string{
	"2006-01-02 15:04:05.999999999 -07:00",
	"2006-01-02 15:04:05.999999999-07:00",
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

func normalizeTime(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case time.Time:
		return t.UTC()
	case []byte:
		return parseTime(string(t))
	case string:
		return parseTime(t)
	default:
		return fmt.Sprint(v)
	}
}

func parseTime(s string) any {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return s
}

// ParseBound resolves a --since/--until value: a duration with an optional
// "d" suffix (7d, 24h, 90m) counted back from now, a date (2026-09-01), or an
// RFC3339 timestamp. Dates are interpreted as midnight UTC.
func ParseBound(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time bound")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UTC(), nil
	}
	d, err := parseDurationDays(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time bound %q: want a duration (7d, 24h), a date (2026-09-01) or an RFC3339 timestamp", s)
	}
	return now.Add(-d).UTC(), nil
}

func parseDurationDays(s string) (time.Duration, error) {
	if n := len(s); n > 1 && s[n-1] == 'd' {
		var days float64
		if _, err := fmt.Sscanf(s[:n-1], "%g", &days); err != nil {
			return 0, err
		}
		if days <= 0 {
			return 0, fmt.Errorf("must be positive")
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return d, nil
}
