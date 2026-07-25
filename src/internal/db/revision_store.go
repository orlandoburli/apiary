package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
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

// NewRevisionID returns a collision-resistant ID for config_revisions rows,
// using the same millisecond-timestamp + random-bytes scheme as task IDs.
func NewRevisionID() string {
	ts := uint64(time.Now().UnixMilli())
	var r [10]byte
	_, _ = rand.Read(r[:])
	return fmt.Sprintf("rev_%012x%s", ts&0xFFFFFFFFFFFF, hex.EncodeToString(r[:]))
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

// FindConfigRevisionByDigestPrefix returns the most recent revision whose
// config_digest starts with prefix, scoped to the given environment. Returns
// nil (no error) when no matching row exists.
func (c *Client) FindConfigRevisionByDigestPrefix(ctx context.Context, environment, prefix string) (*ConfigRevision, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, environment, config_digest, COALESCE(git_revision,''), event,
		       COALESCE(from_environment,''), COALESCE(note,''), recorded_at
		FROM config_revisions
		WHERE environment = ? AND config_digest LIKE ?
		ORDER BY recorded_at DESC LIMIT 1
	`, environment, strings.ReplaceAll(prefix, "%", "\\%")+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var r ConfigRevision
	if err := rows.Scan(&r.ID, &r.Environment, &r.ConfigDigest, &r.GitRevision,
		&r.Event, &r.FromEnvironment, &r.Note, &r.RecordedAt); err != nil {
		return nil, err
	}
	return &r, rows.Err()
}
