package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"powerposter/internal/media"
)

// ---- Renditions ----

type Rendition struct {
	ID          int64       `json:"id"`
	AssetID     int64       `json:"assetId"`
	Platform    string      `json:"platform"`
	ProfileHash string      `json:"profileHash"`
	Plan        media.Plan  `json:"plan"`
	Path        string      `json:"-"`
	Info        *media.Info `json:"info"`
	Status      string      `json:"status"`
	Progress    float64     `json:"progress"`
	Checksum    string      `json:"checksum"`
	Error       string      `json:"error"`
}

const renditionCols = `id, asset_id, platform, profile_hash, plan, path, info, status, progress, checksum, error`

func scanRendition(row interface{ Scan(...any) error }) (Rendition, error) {
	var r Rendition
	var plan string
	var info sql.NullString
	err := row.Scan(&r.ID, &r.AssetID, &r.Platform, &r.ProfileHash, &plan, &r.Path, &info, &r.Status, &r.Progress, &r.Checksum, &r.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	_ = json.Unmarshal([]byte(plan), &r.Plan)
	if info.Valid {
		var i media.Info
		if json.Unmarshal([]byte(info.String), &i) == nil {
			r.Info = &i
		}
	}
	return r, nil
}

func (s *Store) UpsertRendition(ctx context.Context, r *Rendition) error {
	var info any
	if r.Info != nil {
		info = mustJSON(r.Info)
	}
	return s.db.QueryRowContext(ctx, `INSERT INTO renditions (asset_id, platform, profile_hash, plan, path, info, status, progress, checksum, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(asset_id, platform) DO UPDATE SET profile_hash = excluded.profile_hash, plan = excluded.plan, path = excluded.path,
			info = excluded.info, status = excluded.status, progress = excluded.progress, checksum = excluded.checksum, error = excluded.error
		RETURNING id`,
		r.AssetID, r.Platform, r.ProfileHash, mustJSON(r.Plan), r.Path, info, r.Status, r.Progress, r.Checksum, r.Error).Scan(&r.ID)
}

func (s *Store) ListRenditions(ctx context.Context, assetID int64) ([]Rendition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+renditionCols+` FROM renditions WHERE asset_id = ? ORDER BY id`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rendition{}
	for rows.Next() {
		r, err := scanRendition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FindRenditionByPath returns any rendition that produces path (they share the file).
func (s *Store) FindRenditionByPath(ctx context.Context, path string) (Rendition, error) {
	return scanRendition(s.db.QueryRowContext(ctx, `SELECT `+renditionCols+` FROM renditions WHERE path = ?
		ORDER BY CASE status WHEN 'ready' THEN 0 WHEN 'processing' THEN 1 WHEN 'pending' THEN 2 ELSE 3 END LIMIT 1`, path))
}

func (s *Store) CountRenditionsByPath(ctx context.Context, path string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM renditions WHERE path = ?`, path).Scan(&n)
	return n, err
}

// SetRenditionProgress updates all renditions sharing path.
func (s *Store) SetRenditionProgress(ctx context.Context, path, status string, progress float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE renditions SET status = ?, progress = ?, error = '' WHERE path = ?`, status, progress, path)
	return err
}

func (s *Store) SetRenditionDone(ctx context.Context, path string, info media.Info, checksum string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE renditions SET status = 'ready', progress = 1, info = ?, checksum = ?, error = '' WHERE path = ?`,
		mustJSON(info), checksum, path)
	return err
}

func (s *Store) SetRenditionFailed(ctx context.Context, path, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE renditions SET status = 'failed', error = ? WHERE path = ?`, errMsg, path)
	return err
}

func (s *Store) DeleteRendition(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM renditions WHERE id = ?`, id)
	return err
}

// ---- Jobs ----

type Job struct {
	ID      int64
	Type    string
	Payload json.RawMessage
}

func (s *Store) EnqueueJob(ctx context.Context, typ string, payload any) (int64, error) {
	ts := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO jobs (type, payload, created_at, updated_at) VALUES (?, ?, ?, ?)`, typ, mustJSON(payload), ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ClaimJob atomically marks the oldest queued job of a type as running.
func (s *Store) ClaimJob(ctx context.Context, typ string) (Job, bool, error) {
	var j Job
	var payload string
	err := s.db.QueryRowContext(ctx, `UPDATE jobs SET status = 'running', updated_at = ?
		WHERE id = (SELECT id FROM jobs WHERE status = 'queued' AND type = ? ORDER BY id LIMIT 1)
		RETURNING id, type, payload`, now(), typ).Scan(&j.ID, &j.Type, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return j, false, nil
	}
	if err != nil {
		return j, false, err
	}
	j.Payload = json.RawMessage(payload)
	return j, true, nil
}

func (s *Store) FinishJob(ctx context.Context, id int64, jobErr error) error {
	status, msg := "done", ""
	if jobErr != nil {
		status, msg = "failed", jobErr.Error()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ?, error = ?, updated_at = ? WHERE id = ?`, status, msg, now(), id)
	return err
}

// RecoverJobs handles jobs left "running" by a crash: jobs of the requeue types go
// back to the queue, the rest are failed and returned so callers can clean up.
func (s *Store) RecoverJobs(ctx context.Context, requeue ...string) ([]Job, error) {
	for _, t := range requeue {
		if _, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued' WHERE status = 'running' AND type = ?`, t); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `UPDATE jobs SET status = 'failed', error = 'interrupted by restart', updated_at = ?
		WHERE status = 'running' RETURNING id, type, payload`, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		var payload string
		if err := rows.Scan(&j.ID, &j.Type, &payload); err != nil {
			return nil, err
		}
		j.Payload = json.RawMessage(payload)
		out = append(out, j)
	}
	return out, rows.Err()
}

// ---- Connections ----

type Connection struct {
	Platform         string
	AccountID        string
	AccountName      string
	AccessTokenEnc   []byte
	RefreshTokenEnc  []byte
	ExpiresAt        *time.Time
	RefreshExpiresAt *time.Time
	Scopes           string
	UpdatedAt        time.Time
}

func (s *Store) SaveConnection(ctx context.Context, c Connection) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO connections (platform, account_id, account_name, access_token_enc, refresh_token_enc, expires_at, refresh_expires_at, scopes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform) DO UPDATE SET account_id = excluded.account_id, account_name = excluded.account_name,
			access_token_enc = excluded.access_token_enc, refresh_token_enc = excluded.refresh_token_enc, expires_at = excluded.expires_at,
			refresh_expires_at = excluded.refresh_expires_at, scopes = excluded.scopes, updated_at = excluded.updated_at`,
		c.Platform, c.AccountID, c.AccountName, c.AccessTokenEnc, c.RefreshTokenEnc, formatNullTime(c.ExpiresAt), formatNullTime(c.RefreshExpiresAt), c.Scopes, now())
	return err
}

func (s *Store) GetConnection(ctx context.Context, platform string) (Connection, error) {
	var c Connection
	var exp, rexp sql.NullString
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT platform, account_id, account_name, access_token_enc, refresh_token_enc, expires_at, refresh_expires_at, scopes, updated_at
		FROM connections WHERE platform = ?`, platform).
		Scan(&c.Platform, &c.AccountID, &c.AccountName, &c.AccessTokenEnc, &c.RefreshTokenEnc, &exp, &rexp, &c.Scopes, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.ExpiresAt, c.RefreshExpiresAt = parseNullTime(exp), parseNullTime(rexp)
	c.UpdatedAt = parseTime(updated)
	return c, nil
}

func (s *Store) DeleteConnection(ctx context.Context, platform string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE platform = ?`, platform)
	return err
}

func (s *Store) SaveOAuthState(ctx context.Context, state, platform, verifier string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO oauth_states (state, platform, verifier, created_at) VALUES (?, ?, ?, ?)`, state, platform, verifier, now())
	return err
}

// TakeOAuthState consumes a state value (younger than maxAge) and returns its platform and PKCE verifier.
func (s *Store) TakeOAuthState(ctx context.Context, state string, maxAge time.Duration) (platform, verifier string, err error) {
	var created string
	err = s.db.QueryRowContext(ctx, `DELETE FROM oauth_states WHERE state = ? RETURNING platform, verifier, created_at`, state).Scan(&platform, &verifier, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if time.Since(parseTime(created)) > maxAge {
		return "", "", ErrNotFound
	}
	return platform, verifier, nil
}
