package db

import (
	"context"
	"time"
)

// ConfigRevision is one row in the config_revisions audit table.
type ConfigRevision struct {
	ID              string
	Environment     string
	ConfigDigest    string
	GitRevision     string
	Event           string // startup | promote | rollback
	FromEnvironment string // set on promote/rollback
	Note            string
	RecordedAt      time.Time
}

// RecordConfigRevision inserts a new audit entry.
func (c *Client) RecordConfigRevision(ctx context.Context, rev *ConfigRevision) error {
	if rev.RecordedAt.IsZero() {
		rev.RecordedAt = time.Now()
	}
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO config_revisions
		  (id, environment, config_digest, git_revision, event, from_environment, note, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, rev.ID, rev.Environment, rev.ConfigDigest, rev.GitRevision,
		rev.Event, nullStr(rev.FromEnvironment), nullStr(rev.Note), rev.RecordedAt)
	return err
}

// ListConfigRevisions returns revisions for an environment (newest first).
// Pass environment="" to list revisions for the base config.
// Pass limit<=0 for a default of 20.
func (c *Client) ListConfigRevisions(ctx context.Context, environment string, limit int) ([]ConfigRevision, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, environment, config_digest, COALESCE(git_revision,''), event,
		       COALESCE(from_environment,''), COALESCE(note,''), recorded_at
		FROM config_revisions
		WHERE environment = ?
		ORDER BY recorded_at DESC LIMIT ?
	`, environment, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigRevision
	for rows.Next() {
		var r ConfigRevision
		if err := rows.Scan(&r.ID, &r.Environment, &r.ConfigDigest, &r.GitRevision,
			&r.Event, &r.FromEnvironment, &r.Note, &r.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetConfigRevisionByDigest returns the most recent revision matching a digest.
func (c *Client) GetConfigRevisionByDigest(ctx context.Context, digest string) (*ConfigRevision, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT id, environment, config_digest, COALESCE(git_revision,''), event,
		       COALESCE(from_environment,''), COALESCE(note,''), recorded_at
		FROM config_revisions
		WHERE config_digest = ?
		ORDER BY recorded_at DESC LIMIT 1
	`, digest)
	var r ConfigRevision
	err := row.Scan(&r.ID, &r.Environment, &r.ConfigDigest, &r.GitRevision,
		&r.Event, &r.FromEnvironment, &r.Note, &r.RecordedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
