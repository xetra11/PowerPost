package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"powerposter/internal/media"
)

type Post struct {
	ID             int64     `json:"id"`
	Title          string    `json:"title"`
	Kind           string    `json:"kind"`
	Caption        string    `json:"caption"`
	Hashtags       []string  `json:"hashtags"`
	AspectStrategy string    `json:"aspectStrategy"`
	CoverMs        int64     `json:"coverMs"`
	CoverPath      string    `json:"-"`           // JPEG shown as cover (frame or custom image)
	CoverCustom    bool      `json:"coverCustom"` // CoverPath is an uploaded image, not a video frame
	Status         string    `json:"status"`
	ApprovedHash   string    `json:"-"`
	CreatedBy      string    `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

const postCols = `id, title, kind, caption, hashtags, aspect_strategy, cover_ms, cover_path, cover_custom, status, approved_hash, created_by, created_at, updated_at`

func scanPost(row interface{ Scan(...any) error }) (Post, error) {
	var p Post
	var tags, created, updated string
	err := row.Scan(&p.ID, &p.Title, &p.Kind, &p.Caption, &tags, &p.AspectStrategy, &p.CoverMs, &p.CoverPath, &p.CoverCustom, &p.Status, &p.ApprovedHash, &p.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	_ = json.Unmarshal([]byte(tags), &p.Hashtags)
	if p.Hashtags == nil {
		p.Hashtags = []string{}
	}
	p.CreatedAt, p.UpdatedAt = parseTime(created), parseTime(updated)
	return p, nil
}

func (s *Store) CreatePost(ctx context.Context, p *Post) error {
	ts := now()
	if p.Hashtags == nil {
		p.Hashtags = []string{}
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO posts (title, kind, caption, hashtags, aspect_strategy, cover_ms, cover_path, cover_custom, status, approved_hash, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Title, p.Kind, p.Caption, mustJSON(p.Hashtags), p.AspectStrategy, p.CoverMs, p.CoverPath, p.CoverCustom, p.Status, p.ApprovedHash, p.CreatedBy, ts, ts)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt, p.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) GetPost(ctx context.Context, id int64) (Post, error) {
	return scanPost(s.db.QueryRowContext(ctx, `SELECT `+postCols+` FROM posts WHERE id = ?`, id))
}

// ListPosts returns posts, newest first. Empty status means all.
func (s *Store) ListPosts(ctx context.Context, status string) ([]Post, error) {
	q := `SELECT ` + postCols + ` FROM posts`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY updated_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Post{}
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdatePost(ctx context.Context, p *Post) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `UPDATE posts SET title = ?, kind = ?, caption = ?, hashtags = ?, aspect_strategy = ?, cover_ms = ?, cover_path = ?, cover_custom = ?,
		status = ?, approved_hash = ?, updated_at = ? WHERE id = ?`,
		p.Title, p.Kind, p.Caption, mustJSON(p.Hashtags), p.AspectStrategy, p.CoverMs, p.CoverPath, p.CoverCustom, p.Status, p.ApprovedHash, ts, p.ID)
	p.UpdatedAt = parseTime(ts)
	return err
}

func (s *Store) DeletePost(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM posts WHERE id = ?`, id)
	return err
}

// ---- Assets ----

type Asset struct {
	ID           int64      `json:"id"`
	PostID       int64      `json:"postId"`
	Path         string     `json:"-"`
	OriginalName string     `json:"originalName"`
	ThumbPath    string     `json:"-"`
	Info         media.Info `json:"info"`
	CreatedAt    time.Time  `json:"createdAt"`
}

func (s *Store) CreateAsset(ctx context.Context, a *Asset) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO assets (post_id, path, original_name, thumb_path, info, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.PostID, a.Path, a.OriginalName, a.ThumbPath, mustJSON(a.Info), ts)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	a.CreatedAt = parseTime(ts)
	return nil
}

func (s *Store) ListAssets(ctx context.Context, postID int64) ([]Asset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, post_id, path, original_name, thumb_path, info, created_at FROM assets WHERE post_id = ? ORDER BY id`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var a Asset
		var info, created string
		if err := rows.Scan(&a.ID, &a.PostID, &a.Path, &a.OriginalName, &a.ThumbPath, &info, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(info), &a.Info)
		a.CreatedAt = parseTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAsset(ctx context.Context, id int64) (Asset, error) {
	var a Asset
	var info, created string
	err := s.db.QueryRowContext(ctx, `SELECT id, post_id, path, original_name, thumb_path, info, created_at FROM assets WHERE id = ?`, id).
		Scan(&a.ID, &a.PostID, &a.Path, &a.OriginalName, &a.ThumbPath, &info, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	_ = json.Unmarshal([]byte(info), &a.Info)
	a.CreatedAt = parseTime(created)
	return a, nil
}

func (s *Store) DeleteAsset(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id)
	return err
}

// ---- Targets ----

type Target struct {
	ID               int64           `json:"id"`
	PostID           int64           `json:"postId"`
	Platform         string          `json:"platform"`
	Enabled          bool            `json:"enabled"`
	CaptionOverride  *string         `json:"captionOverride"`
	HashtagsOverride *[]string       `json:"hashtagsOverride"`
	Options          json.RawMessage `json:"options"`
	Status           string          `json:"status"`
	Stage            string          `json:"stage"`
	RemoteID         string          `json:"remoteId"`
	Permalink        string          `json:"permalink"`
	Note             string          `json:"note"`
	Error            string          `json:"error"`
	PublishedAt      *time.Time      `json:"publishedAt"`
}

// UpsertTarget writes the editable fields of a target.
func (s *Store) UpsertTarget(ctx context.Context, t *Target) error {
	var tags any
	if t.HashtagsOverride != nil {
		tags = mustJSON(*t.HashtagsOverride)
	}
	if len(t.Options) == 0 {
		t.Options = json.RawMessage(`{}`)
	}
	err := s.db.QueryRowContext(ctx, `INSERT INTO targets (post_id, platform, enabled, caption_override, hashtags_override, options)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(post_id, platform) DO UPDATE SET enabled = excluded.enabled, caption_override = excluded.caption_override,
			hashtags_override = excluded.hashtags_override, options = excluded.options
		RETURNING id`,
		t.PostID, t.Platform, t.Enabled, t.CaptionOverride, tags, string(t.Options)).Scan(&t.ID)
	return err
}

func (s *Store) ListTargets(ctx context.Context, postID int64) ([]Target, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, post_id, platform, enabled, caption_override, hashtags_override, options,
		status, stage, remote_id, permalink, note, error, published_at FROM targets WHERE post_id = ? ORDER BY id`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var t Target
		var caption, tags, published sql.NullString
		var opts string
		if err := rows.Scan(&t.ID, &t.PostID, &t.Platform, &t.Enabled, &caption, &tags, &opts,
			&t.Status, &t.Stage, &t.RemoteID, &t.Permalink, &t.Note, &t.Error, &published); err != nil {
			return nil, err
		}
		if caption.Valid {
			t.CaptionOverride = &caption.String
		}
		if tags.Valid {
			var list []string
			_ = json.Unmarshal([]byte(tags.String), &list)
			if list == nil {
				list = []string{}
			}
			t.HashtagsOverride = &list
		}
		t.Options = json.RawMessage(opts)
		t.PublishedAt = parseNullTime(published)
		out = append(out, t)
	}
	return out, rows.Err()
}

// TargetState is the publish-progress part of a target.
type TargetState struct {
	Status, Stage, RemoteID, Permalink, Note, Error string
	PublishedAt                                     *time.Time
}

func (s *Store) SetTargetState(ctx context.Context, id int64, st TargetState) error {
	_, err := s.db.ExecContext(ctx, `UPDATE targets SET status = ?, stage = ?, remote_id = ?, permalink = ?, note = ?, error = ?, published_at = ? WHERE id = ?`,
		st.Status, st.Stage, st.RemoteID, st.Permalink, st.Note, st.Error, formatNullTime(st.PublishedAt), id)
	return err
}

func (s *Store) SetTargetStage(ctx context.Context, id int64, stage string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE targets SET stage = ? WHERE id = ?`, stage, id)
	return err
}

// CountTargetsByStatus counts the enabled targets of a post per publish status.
func (s *Store) CountTargetsByStatus(ctx context.Context, postID int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM targets WHERE post_id = ? AND enabled = 1 GROUP BY status`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// ---- Publish attempts ----

func (s *Store) StartAttempt(ctx context.Context, targetID int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO publish_attempts (target_id, started_at) VALUES (?, ?)`, targetID, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishAttempt(ctx context.Context, id int64, status, errMsg, log string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE publish_attempts SET finished_at = ?, status = ?, error = ?, log = ? WHERE id = ?`, now(), status, errMsg, log, id)
	return err
}

type Attempt struct {
	ID         int64      `json:"id"`
	TargetID   int64      `json:"targetId"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Status     string     `json:"status"`
	Error      string     `json:"error"`
	Log        string     `json:"log"`
}

func (s *Store) ListAttempts(ctx context.Context, postID int64) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.target_id, a.started_at, a.finished_at, a.status, a.error, a.log
		FROM publish_attempts a JOIN targets t ON t.id = a.target_id WHERE t.post_id = ? ORDER BY a.id DESC`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attempt{}
	for rows.Next() {
		var a Attempt
		var started string
		var finished sql.NullString
		if err := rows.Scan(&a.ID, &a.TargetID, &started, &finished, &a.Status, &a.Error, &a.Log); err != nil {
			return nil, err
		}
		a.StartedAt = parseTime(started)
		a.FinishedAt = parseNullTime(finished)
		out = append(out, a)
	}
	return out, rows.Err()
}
