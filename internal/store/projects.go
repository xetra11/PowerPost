package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Project struct {
	ID        int64           `json:"id"`
	Slug      string          `json:"slug"`
	Name      string          `json:"name"`
	Settings  json.RawMessage `json:"-"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ErrDuplicate reports a unique constraint violation (e.g. a taken slug).
var ErrDuplicate = errors.New("already exists")

const projectCols = `id, slug, name, settings, created_at`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var settings, created string
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &settings, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Settings = json.RawMessage(settings)
	p.CreatedAt = parseTime(created)
	return p, nil
}

func uniqueErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrDuplicate
	}
	return err
}

func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	ts := now()
	if len(p.Settings) == 0 {
		p.Settings = json.RawMessage(`{}`)
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO projects (slug, name, settings, created_at) VALUES (?, ?, ?, ?)`, p.Slug, p.Name, string(p.Settings), ts)
	if err != nil {
		return uniqueErr(err)
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt = parseTime(ts)
	return nil
}

func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
}

func (s *Store) GetProjectBySlug(ctx context.Context, slug string) (Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE slug = ?`, slug))
}

// ListProjects returns all projects in creation order.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectCols+` FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProject(ctx context.Context, p *Project) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET slug = ?, name = ?, settings = ? WHERE id = ?`, p.Slug, p.Name, string(p.Settings), p.ID)
	return uniqueErr(err)
}

// DeleteProject removes a project and its connections (posts must be gone already).
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	return err
}

func (s *Store) CountPosts(ctx context.Context, projectID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM posts WHERE project_id = ?`, projectID).Scan(&n)
	return n, err
}
