package improve

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Finding states in the ledger.
const (
	FindingProposed = "proposed"
	FindingApplied  = "applied"
	FindingRejected = "rejected"
	FindingReverted = "reverted"
)

// LedgerRun is one recorded `apiary improve` invocation.
type LedgerRun struct {
	ID             string
	Effort         string
	Focus          string
	WindowStart    time.Time
	WindowEnd      time.Time
	Scope          string
	EvidenceDigest string
	AdvisorAgent   string
	AdvisorRunner  string
	AdvisorModel   string
	ReportPath     string
	Applied        bool
	AppliedAt      *time.Time
	CostUSD        float64
	TotalTokens    int
	CreatedAt      time.Time
}

// LedgerFinding is one recorded proposal, with the metrics that justified it.
type LedgerFinding struct {
	ID              string
	RunID           string
	FindingID       string
	Scope           string
	Focus           string
	Severity        string
	Confidence      string
	Symptom         string
	Rationale       string
	TargetFile      string
	BaselineMetrics string
	Patch           string
	MachineChecked  bool
	State           string
	RejectReason    string
}

// Ledger writes and reads the improvement history.
//
// It holds its own read-write connection. The analysis path opens the database
// read-only on purpose — it must never migrate or lock a database a running
// daemon depends on — so the two cannot share a handle, and the writer is only
// opened when there is something to record.
type Ledger struct {
	db *sql.DB
}

// OpenLedger opens the database for writing and ensures the ledger tables exist.
func OpenLedger(ctx context.Context, dbPath string) (*Ledger, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_time_format=sqlite",
		url.PathEscape(dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open ledger %s: %w", dbPath, err)
	}
	l := &Ledger{db: db}
	if err := l.ensureTables(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return l, nil
}

func (l *Ledger) Close() error { return l.db.Close() }

// ensureTables creates the ledger tables when the database predates them. The
// daemon's migration list covers the normal case; this covers a database that
// has not been opened by a daemon carrying the migration yet, so `apiary
// improve` never fails merely because nothing has migrated it.
func (l *Ledger) ensureTables(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS improvement_runs (
		  id TEXT PRIMARY KEY, effort TEXT NOT NULL, focus TEXT,
		  window_start TIMESTAMP, window_end TIMESTAMP, scope TEXT,
		  evidence_digest TEXT, advisor_agent TEXT, advisor_runner TEXT,
		  advisor_model TEXT, report_path TEXT,
		  applied BOOLEAN NOT NULL DEFAULT 0, applied_at TIMESTAMP,
		  cost_usd REAL NOT NULL DEFAULT 0, total_tokens INTEGER NOT NULL DEFAULT 0,
		  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS improvement_findings (
		  id TEXT PRIMARY KEY, run_id TEXT NOT NULL, finding_id TEXT,
		  scope TEXT NOT NULL, focus TEXT, severity TEXT, confidence TEXT,
		  symptom TEXT, rationale TEXT, target_file TEXT,
		  baseline_metrics TEXT, patch TEXT,
		  machine_checked BOOLEAN NOT NULL DEFAULT 0,
		  state TEXT NOT NULL, reject_reason TEXT,
		  FOREIGN KEY(run_id) REFERENCES improvement_runs(id))`,
		`CREATE INDEX IF NOT EXISTS idx_improvement_findings_run ON improvement_findings(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_improvement_findings_scope ON improvement_findings(scope, state)`,
		`CREATE INDEX IF NOT EXISTS idx_improvement_runs_created ON improvement_runs(created_at DESC)`,
	}
	for _, s := range stmts {
		if _, err := l.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("create ledger tables: %w", err)
		}
	}
	return nil
}

// RecordRun writes a run and its findings in one transaction, so a partial
// record can never look like a complete one.
func (l *Ledger) RecordRun(ctx context.Context, run LedgerRun, findings []LedgerFinding) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ledger transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO improvement_runs
		  (id, effort, focus, window_start, window_end, scope, evidence_digest,
		   advisor_agent, advisor_runner, advisor_model, report_path,
		   applied, applied_at, cost_usd, total_tokens, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.Effort, run.Focus, run.WindowStart, run.WindowEnd, run.Scope,
		run.EvidenceDigest, run.AdvisorAgent, run.AdvisorRunner, run.AdvisorModel,
		run.ReportPath, run.Applied, run.AppliedAt, run.CostUSD, run.TotalTokens,
		run.CreatedAt); err != nil {
		return fmt.Errorf("insert improvement run: %w", err)
	}

	for _, f := range findings {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO improvement_findings
			  (id, run_id, finding_id, scope, focus, severity, confidence, symptom,
			   rationale, target_file, baseline_metrics, patch, machine_checked,
			   state, reject_reason)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			f.ID, run.ID, f.FindingID, f.Scope, f.Focus, f.Severity, f.Confidence,
			f.Symptom, f.Rationale, f.TargetFile, f.BaselineMetrics, f.Patch,
			f.MachineChecked, f.State, f.RejectReason); err != nil {
			return fmt.Errorf("insert improvement finding: %w", err)
		}
	}
	return tx.Commit()
}

// ListRuns returns recent runs, newest first.
func (l *Ledger) ListRuns(ctx context.Context, limit int) ([]LedgerRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := l.db.QueryContext(ctx, `
		SELECT id, effort, COALESCE(focus,''), window_start, window_end,
		       COALESCE(evidence_digest,''), COALESCE(advisor_agent,''),
		       COALESCE(advisor_runner,''), COALESCE(advisor_model,''),
		       COALESCE(report_path,''), applied, applied_at,
		       cost_usd, total_tokens, created_at
		FROM improvement_runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list improvement runs: %w", err)
	}
	defer rows.Close()

	var out []LedgerRun
	for rows.Next() {
		var r LedgerRun
		var appliedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.Effort, &r.Focus, &r.WindowStart, &r.WindowEnd,
			&r.EvidenceDigest, &r.AdvisorAgent, &r.AdvisorRunner, &r.AdvisorModel,
			&r.ReportPath, &r.Applied, &appliedAt, &r.CostUSD, &r.TotalTokens,
			&r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan improvement run: %w", err)
		}
		if appliedAt.Valid {
			t := appliedAt.Time
			r.AppliedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRun returns one run and its findings.
func (l *Ledger) GetRun(ctx context.Context, id string) (*LedgerRun, []LedgerFinding, error) {
	runs, err := l.ListRuns(ctx, 1000)
	if err != nil {
		return nil, nil, err
	}
	var run *LedgerRun
	for i := range runs {
		if runs[i].ID == id {
			run = &runs[i]
			break
		}
	}
	if run == nil {
		return nil, nil, fmt.Errorf("no improvement run %q", id)
	}

	rows, err := l.db.QueryContext(ctx, `
		SELECT id, run_id, COALESCE(finding_id,''), scope, COALESCE(focus,''),
		       COALESCE(severity,''), COALESCE(confidence,''), COALESCE(symptom,''),
		       COALESCE(rationale,''), COALESCE(target_file,''),
		       COALESCE(baseline_metrics,''), COALESCE(patch,''),
		       machine_checked, state, COALESCE(reject_reason,'')
		FROM improvement_findings WHERE run_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("list improvement findings: %w", err)
	}
	defer rows.Close()

	var findings []LedgerFinding
	for rows.Next() {
		var f LedgerFinding
		if err := rows.Scan(&f.ID, &f.RunID, &f.FindingID, &f.Scope, &f.Focus,
			&f.Severity, &f.Confidence, &f.Symptom, &f.Rationale, &f.TargetFile,
			&f.BaselineMetrics, &f.Patch, &f.MachineChecked, &f.State,
			&f.RejectReason); err != nil {
			return nil, nil, fmt.Errorf("scan improvement finding: %w", err)
		}
		findings = append(findings, f)
	}
	return run, findings, rows.Err()
}

// PriorFindings returns the applied findings from earlier runs, so the advisor
// can be told what has already been tried. Without this it re-proposes the same
// change every run, and a suggestion that was applied and did not help looks
// identical to one that was never tried.
func (l *Ledger) PriorFindings(ctx context.Context, limit int) ([]LedgerFinding, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := l.db.QueryContext(ctx, `
		SELECT f.id, f.run_id, COALESCE(f.finding_id,''), f.scope, COALESCE(f.focus,''),
		       COALESCE(f.severity,''), COALESCE(f.confidence,''), COALESCE(f.symptom,''),
		       COALESCE(f.rationale,''), COALESCE(f.target_file,''),
		       COALESCE(f.baseline_metrics,''), '', f.machine_checked, f.state,
		       COALESCE(f.reject_reason,'')
		FROM improvement_findings f
		JOIN improvement_runs r ON r.id = f.run_id
		WHERE f.state IN ('applied','reverted')
		ORDER BY r.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list prior findings: %w", err)
	}
	defer rows.Close()

	var out []LedgerFinding
	for rows.Next() {
		var f LedgerFinding
		if err := rows.Scan(&f.ID, &f.RunID, &f.FindingID, &f.Scope, &f.Focus,
			&f.Severity, &f.Confidence, &f.Symptom, &f.Rationale, &f.TargetFile,
			&f.BaselineMetrics, &f.Patch, &f.MachineChecked, &f.State,
			&f.RejectReason); err != nil {
			return nil, fmt.Errorf("scan prior finding: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// MarkApplied flips a run and the given findings to applied.
func (l *Ledger) MarkApplied(ctx context.Context, runID string, findingIDs []string, at time.Time) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful commit

	if _, err := tx.ExecContext(ctx,
		`UPDATE improvement_runs SET applied = 1, applied_at = ? WHERE id = ?`, at, runID); err != nil {
		return fmt.Errorf("mark run applied: %w", err)
	}
	for _, id := range findingIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE improvement_findings SET state = ? WHERE run_id = ? AND id = ?`,
			FindingApplied, runID, id); err != nil {
			return fmt.Errorf("mark finding applied: %w", err)
		}
	}
	return tx.Commit()
}

// BaselineFor decodes a finding's stored baseline metrics.
func BaselineFor(f LedgerFinding) (StepMetrics, bool) {
	if f.BaselineMetrics == "" {
		return StepMetrics{}, false
	}
	var m StepMetrics
	if err := json.Unmarshal([]byte(f.BaselineMetrics), &m); err != nil {
		return StepMetrics{}, false
	}
	return m, true
}

// NewRunID derives a sortable run identifier from a timestamp. Improvement runs
// are operator-initiated and never concurrent, so second resolution is enough
// and a readable id is worth more than a ULID here.
func NewRunID(t time.Time) string {
	return "imp_" + t.UTC().Format("20060102T150405")
}

// FindingRowID is the ledger primary key for one recommendation within a run.
func FindingRowID(runID, recommendationID string) string {
	return runID + ":" + recommendationID
}
