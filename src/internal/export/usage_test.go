package export

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orlandoburli/apiary/internal/db"
	_ "modernc.org/sqlite"
)

var base = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// openReal creates a database with the production schema (every migration
// applied) and returns a read-write connection to seed it. Using the real
// schema is the point: the export contract is tested against the columns the
// daemon actually writes.
func openReal(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "apiary.db")
	client, err := db.New(context.Background(), path)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	conn, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

type execRow struct {
	task, agent, model, runner, status string
	started                            any // time.Time or nil
	instance, step                     string
	cost                               float64
	credit                             int
	errMsg                             string
	prompt, output                     string
}

func seed(t *testing.T, conn *sql.DB, instances [][3]string, execs []execRow) {
	t.Helper()
	for _, wi := range instances {
		if _, err := conn.Exec(`INSERT INTO workflow_instances (id, workflow_id, cell_id, source_id, state) VALUES (?, ?, 'cell', ?, 'done')`,
			wi[0], wi[1], wi[2]); err != nil {
			t.Fatalf("seed instance: %v", err)
		}
	}
	for _, e := range execs {
		var inst any
		if e.instance != "" {
			inst = e.instance
		}
		if _, err := conn.Exec(`INSERT INTO task_executions
			(task_id, agent_id, title, task_number, task_url, model, runner, attempt, status, started_at,
			 workflow_instance_id, step_id, input_tokens, output_tokens, total_tokens, cost_usd,
			 credit_exhausted, error_message, input_prompt, output_text, time_thinking_ms)
			VALUES (?, ?, 'Title', 'ERP-1', 'https://x/1', ?, ?, 1, ?, ?, ?, ?, 100, 50, 150, ?, ?, ?, ?, ?, 7)`,
			e.task, e.agent, e.model, e.runner, e.status, e.started, inst, e.step, e.cost, e.credit,
			e.errMsg, e.prompt, e.output); err != nil {
			t.Fatalf("seed execution: %v", err)
		}
	}
}

func collect(t *testing.T, conn Querier, f UsageFilter) []Row {
	t.Helper()
	var rows []Row
	if err := ListUsageRows(context.Background(), conn, f, func(r Row) error {
		rows = append(rows, r)
		return nil
	}); err != nil {
		t.Fatalf("ListUsageRows: %v", err)
	}
	return rows
}

func col(t *testing.T, f UsageFilter, name string) int {
	t.Helper()
	for i, c := range f.Columns() {
		if c.Name == name {
			return i
		}
	}
	t.Fatalf("column %s not selected", name)
	return -1
}

func standardSeed(t *testing.T) *sql.DB {
	conn := openReal(t)
	seed(t, conn,
		[][3]string{{"wi1", "implement", "github"}, {"wi2", "review", "jira"}},
		[]execRow{
			{task: "t1", agent: "eng", model: "m1", runner: "claude-cli", status: "success", started: base, instance: "wi1", step: "plan", cost: 0.5},
			{task: "t1", agent: "eng", model: "m1", runner: "claude-cli", status: "failed", started: base.Add(time.Hour), instance: "wi1", step: "impl", cost: 1.25, credit: 1, errMsg: "boom\nline two"},
			{task: "t2", agent: "rev", model: "m2", runner: "cursor-cli", status: "success", started: base.Add(2 * time.Hour), instance: "wi2", step: "review", cost: 0.1},
			// Pre-workflow row: no instance, must still export with empty workflow columns.
			{task: "t0", agent: "eng", model: "m1", runner: "claude-cli", status: "success", started: base.Add(-24 * time.Hour), cost: 2},
			// Never started.
			{task: "t3", agent: "eng", model: "m1", runner: "claude-cli", status: "pending", started: nil, instance: "wi1", step: "plan"},
		})
	return conn
}

func TestListUsageRowsDefaultExportsStartedRowsOldestFirstWithContext(t *testing.T) {
	conn := standardSeed(t)
	f := UsageFilter{}
	rows := collect(t, conn, f)
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4 (pending excluded)", len(rows))
	}
	task := col(t, f, "task_id")
	if rows[0][task] != "t0" || rows[3][task] != "t2" {
		t.Errorf("order = %v ... %v, want t0 first, t2 last", rows[0][task], rows[3][task])
	}
	// Workflow context comes through the join.
	if rows[1][col(t, f, "workflow_id")] != "implement" || rows[1][col(t, f, "source_id")] != "github" {
		t.Errorf("row 1 context = %v / %v", rows[1][col(t, f, "workflow_id")], rows[1][col(t, f, "source_id")])
	}
	if rows[1][col(t, f, "instance_state")] != "done" {
		t.Errorf("instance_state = %v", rows[1][col(t, f, "instance_state")])
	}
	// Pre-workflow row keeps its numbers and has null context.
	if rows[0][col(t, f, "workflow_id")] != nil || rows[0][col(t, f, "cost_usd")] != 2.0 {
		t.Errorf("pre-workflow row = wf %v cost %v", rows[0][col(t, f, "workflow_id")], rows[0][col(t, f, "cost_usd")])
	}
	// Types by kind.
	if _, ok := rows[0][col(t, f, "started_at")].(time.Time); !ok {
		t.Errorf("started_at is %T, want time.Time", rows[0][col(t, f, "started_at")])
	}
	if rows[2][col(t, f, "credit_exhausted")] != true || rows[0][col(t, f, "credit_exhausted")] != false {
		t.Error("credit_exhausted should decode to bool")
	}
	if rows[0][col(t, f, "time_thinking_ms")] != int64(7) {
		t.Errorf("time_thinking_ms = %v", rows[0][col(t, f, "time_thinking_ms")])
	}
	// Transcripts are not selected by default.
	for _, c := range f.Columns() {
		if c.Name == "input_prompt" || c.Name == "output_text" || c.Name == "slow_tools" {
			t.Errorf("%s selected without opt-in", c.Name)
		}
	}
}

func TestListUsageRowsFilters(t *testing.T) {
	conn := standardSeed(t)
	cases := []struct {
		name string
		f    UsageFilter
		want []string
	}{
		{"window", UsageFilter{Since: base, Until: base.Add(30 * time.Minute)}, []string{"t1"}},
		{"window both", UsageFilter{Since: base, Until: base.Add(90 * time.Minute)}, []string{"t1", "t1"}},
		{"since only", UsageFilter{Since: base.Add(time.Hour)}, []string{"t1", "t2"}},
		{"workflow", UsageFilter{Workflows: []string{"review"}}, []string{"t2"}},
		{"agent", UsageFilter{Agents: []string{"rev"}}, []string{"t2"}},
		{"model", UsageFilter{Models: []string{"m1"}}, []string{"t0", "t1", "t1"}},
		{"source", UsageFilter{Sources: []string{"jira"}}, []string{"t2"}},
		{"status", UsageFilter{Statuses: []string{"failed"}}, []string{"t1"}},
		{"pending opts in NULL start", UsageFilter{Statuses: []string{"pending"}}, []string{"t3"}},
		{"pending with window", UsageFilter{Statuses: []string{"pending", "success"}, Since: base}, []string{"t1", "t2", "t3"}},
		{"repeatable", UsageFilter{Workflows: []string{"implement", "review"}}, []string{"t1", "t1", "t2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := collect(t, conn, tc.f)
			idx := col(t, tc.f, "task_id")
			var got []string
			for _, r := range rows {
				got = append(got, r[idx].(string))
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListUsageRowsTranscriptsOptIn(t *testing.T) {
	conn := openReal(t)
	seed(t, conn, nil, []execRow{{task: "t", agent: "a", status: "success", started: base, prompt: "PROMPT", output: "OUT"}})
	f := UsageFilter{IncludeTranscripts: true, IncludeSlowTools: true}
	rows := collect(t, conn, f)
	if rows[0][col(t, f, "input_prompt")] != "PROMPT" || rows[0][col(t, f, "output_text")] != "OUT" {
		t.Errorf("transcripts = %v / %v", rows[0][col(t, f, "input_prompt")], rows[0][col(t, f, "output_text")])
	}
	if rows[0][col(t, f, "slow_tools")] != nil {
		t.Errorf("slow_tools = %v, want nil", rows[0][col(t, f, "slow_tools")])
	}
}

func TestListUsageRowsToleratesOlderSchema(t *testing.T) {
	// A database written by a binary from before wall-clock attribution and
	// before credit tracking: the export must still run, with those columns null.
	conn, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, s := range []string{
		`CREATE TABLE task_executions (id INTEGER PRIMARY KEY, task_id TEXT, agent_id TEXT, status TEXT, started_at TIMESTAMP, cost_usd REAL)`,
		`CREATE TABLE workflow_instances (id TEXT PRIMARY KEY, workflow_id TEXT, source_id TEXT, state TEXT)`,
		`INSERT INTO task_executions (task_id, agent_id, status, started_at, cost_usd) VALUES ('t', 'a', 'success', '2026-09-01 12:00:00', 0.25)`,
	} {
		if _, err := conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	f := UsageFilter{}
	rows := collect(t, conn, f)
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0][col(t, f, "time_thinking_ms")] != nil || rows[0][col(t, f, "credit_exhausted")] != nil {
		t.Error("missing columns should export as nil")
	}
	if got := rows[0][col(t, f, "started_at")]; got != time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) {
		t.Errorf("started_at = %v (%T)", got, got)
	}
}

func TestUsageColumnsCoverSchema(t *testing.T) {
	// Drift guard: every task_executions column is either exported or listed
	// as deliberately unexported. A new schema column fails here until someone
	// decides which.
	conn := openReal(t)
	present, err := probeColumns(context.Background(), conn, tableExecutions)
	if err != nil {
		t.Fatal(err)
	}
	decided := map[string]bool{}
	for _, c := range UsageColumns {
		if c.table == tableExecutions {
			decided[c.column] = true
		}
	}
	for _, c := range UnexportedExecutionColumns {
		decided[c] = true
	}
	for name := range present[tableExecutions] {
		if !decided[name] {
			t.Errorf("task_executions.%s is neither exported nor in UnexportedExecutionColumns", name)
		}
	}
	// And nothing in the contract points at a column that does not exist.
	for _, c := range UsageColumns {
		if c.table == tableExecutions && !present[tableExecutions][c.column] {
			t.Errorf("contract column %s refers to missing task_executions.%s", c.Name, c.column)
		}
	}
}

func TestCSVWriterFormatsByKind(t *testing.T) {
	f := UsageFilter{}
	cols := f.Columns()
	var buf bytes.Buffer
	w, err := NewCSVWriter(&buf, cols)
	if err != nil {
		t.Fatal(err)
	}
	row := make(Row, len(cols))
	row[col(t, f, "execution_id")] = int64(7)
	row[col(t, f, "cost_usd")] = 1.5
	row[col(t, f, "credit_exhausted")] = true
	row[col(t, f, "started_at")] = base.In(time.FixedZone("BRT", -3*3600))
	row[col(t, f, "error_message")] = "line one\nline  two"
	if err := w.Write(row); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0][0] != "execution_id" {
		t.Fatalf("records = %v", recs)
	}
	got := recs[1]
	checks := map[string]string{
		"execution_id": "7", "cost_usd": "1.500000", "credit_exhausted": "true",
		"started_at": "2026-09-01T12:00:00Z", "error_message": "line one line two", "task_id": "",
	}
	for name, want := range checks {
		if got[col(t, f, name)] != want {
			t.Errorf("%s = %q, want %q", name, got[col(t, f, name)], want)
		}
	}
}

func TestJSONWriterStreamsValidArray(t *testing.T) {
	f := UsageFilter{}
	cols := f.Columns()
	var buf bytes.Buffer
	w := NewJSONWriter(&buf, cols)
	for i := range 2 {
		row := make(Row, len(cols))
		row[col(t, f, "execution_id")] = int64(i)
		row[col(t, f, "started_at")] = base
		row[col(t, f, "credit_exhausted")] = false
		if err := w.Write(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(out) != 2 || out[1]["execution_id"] != float64(1) {
		t.Fatalf("decoded = %v", out)
	}
	if len(out[0]) != len(cols) {
		t.Errorf("object has %d keys, want %d (nulls must be explicit)", len(out[0]), len(cols))
	}
	if out[0]["task_id"] != nil || out[0]["started_at"] != "2026-09-01T12:00:00Z" {
		t.Errorf("task_id = %v, started_at = %v", out[0]["task_id"], out[0]["started_at"])
	}

	// Empty export is still a valid array.
	buf.Reset()
	if err := NewJSONWriter(&buf, cols).Close(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Errorf("empty export = %q", buf.String())
	}
}

func TestParseBound(t *testing.T) {
	now := base
	cases := map[string]time.Time{
		"7d":                   base.Add(-7 * 24 * time.Hour),
		"90m":                  base.Add(-90 * time.Minute),
		"2026-08-01":           time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		"2026-08-01T10:00:00Z": time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := ParseBound(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseBound(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "yesterday", "-3d", "0h"} {
		if _, err := ParseBound(bad, now); err == nil {
			t.Errorf("ParseBound(%q) accepted", bad)
		}
	}
}
