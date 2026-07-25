package db

import (
	"context"
	"database/sql"
	"time"
)

// EnvironmentRevision records one promoted configuration revision for a named
// environment. Written by "apiary promote" after validation and dry-run checks
// pass; read by "apiary rollback" to restore a previous revision.
type EnvironmentRevision struct {
	ID           int64
	EnvName      string
	ConfigDigest string
	GitRevision  string
	// ConfigYAML is the full resolved YAML of the promoted configuration so
	// rollback does not need to reconstruct it from the current config file.
	ConfigYAML string
	PromotedBy string
	CreatedAt  time.Time
}

// SaveEnvironmentRevision appends a new revision record and returns its id.
func (c *Client) SaveEnvironmentRevision(ctx context.Context, r *EnvironmentRevision) (int64, error) {
	res, err := c.db.ExecContext(ctx, `
		INSERT INTO environment_revisions
		  (env_name, config_digest, git_revision, config_yaml, promoted_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, r.EnvName, r.ConfigDigest, r.GitRevision, r.ConfigYAML, r.PromotedBy, time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListEnvironmentRevisions returns the most recent revisions for the given
// environment, newest first. If envName is empty all environments are returned.
func (c *Client) ListEnvironmentRevisions(ctx context.Context, envName string, limit int) ([]EnvironmentRevision, error) {
	if limit <= 0 {
		limit = 20
	}
	var (
		rows *sql.Rows
		err  error
	)
	if envName != "" {
		rows, err = c.db.QueryContext(ctx, `
			SELECT id, env_name, config_digest, COALESCE(git_revision,''),
			       config_yaml, COALESCE(promoted_by,''), created_at
			FROM environment_revisions
			WHERE env_name = ?
			ORDER BY created_at DESC LIMIT ?
		`, envName, limit)
	} else {
		rows, err = c.db.QueryContext(ctx, `
			SELECT id, env_name, config_digest, COALESCE(git_revision,''),
			       config_yaml, COALESCE(promoted_by,''), created_at
			FROM environment_revisions
			ORDER BY created_at DESC LIMIT ?
		`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvironmentRevision
	for rows.Next() {
		var r EnvironmentRevision
		if err := rows.Scan(&r.ID, &r.EnvName, &r.ConfigDigest, &r.GitRevision,
			&r.ConfigYAML, &r.PromotedBy, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetEnvironmentRevisionByDigest returns the most recent revision whose digest
// matches, or (nil, nil) when not found.
func (c *Client) GetEnvironmentRevisionByDigest(ctx context.Context, envName, digest string) (*EnvironmentRevision, error) {
	row := c.db.QueryRowContext(ctx, `
		SELECT id, env_name, config_digest, COALESCE(git_revision,''),
		       config_yaml, COALESCE(promoted_by,''), created_at
		FROM environment_revisions
		WHERE env_name = ? AND config_digest = ?
		ORDER BY created_at DESC LIMIT 1
	`, envName, digest)
	var r EnvironmentRevision
	if err := row.Scan(&r.ID, &r.EnvName, &r.ConfigDigest, &r.GitRevision,
		&r.ConfigYAML, &r.PromotedBy, &r.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}
