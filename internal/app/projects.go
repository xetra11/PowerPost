package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"powerposter/internal/media"
	"powerposter/internal/platform"
	"powerposter/internal/store"
)

// A project is one game or brand with its own brand profile, defaults for
// new posts and platform accounts. Every post belongs to one project.

type Brand struct {
	GameName        string   `json:"gameName"`
	Link            string   `json:"link"`
	DefaultHashtags []string `json:"defaultHashtags"`
	ToneNotes       string   `json:"toneNotes"`
}

type ProjectSettings struct {
	Brand            Brand    `json:"brand"`
	DefaultPlatforms []string `json:"defaultPlatforms"`
	DefaultAspect    string   `json:"defaultAspect"`
}

type ProjectView struct {
	ID        int64           `json:"id"`
	Slug      string          `json:"slug"`
	Name      string          `json:"name"`
	Settings  ProjectSettings `json:"settings"`
	PostCount int             `json:"postCount"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ProjectRef identifies a project in post views and MCP output.
type ProjectRef struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (a *App) projectSettings(p store.Project) ProjectSettings {
	s := ProjectSettings{DefaultAspect: string(media.StrategyBlur)}
	s.DefaultPlatforms = a.platformIDs()
	_ = json.Unmarshal(p.Settings, &s)
	if s.Brand.DefaultHashtags == nil {
		s.Brand.DefaultHashtags = []string{}
	}
	if s.DefaultPlatforms == nil {
		s.DefaultPlatforms = []string{}
	}
	return s
}

func (a *App) projectView(ctx context.Context, p store.Project) (ProjectView, error) {
	n, err := a.Store.CountPosts(ctx, p.ID)
	return ProjectView{ID: p.ID, Slug: p.Slug, Name: p.Name, Settings: a.projectSettings(p), PostCount: n, CreatedAt: p.CreatedAt}, err
}

func (a *App) Projects(ctx context.Context) ([]ProjectView, error) {
	list, err := a.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := []ProjectView{}
	for _, p := range list {
		v, err := a.projectView(ctx, p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (a *App) GetProject(ctx context.Context, id int64) (ProjectView, error) {
	p, err := a.project(ctx, id)
	if err != nil {
		return ProjectView{}, err
	}
	return a.projectView(ctx, p)
}

func (a *App) project(ctx context.Context, id int64) (store.Project, error) {
	p, err := a.Store.GetProject(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return p, notFound("project %d not found", id)
	}
	return p, err
}

// ResolveProject finds a project by slug, name or id. An empty ref picks the
// only project; with several projects the caller has to choose.
func (a *App) ResolveProject(ctx context.Context, ref string) (ProjectView, error) {
	list, err := a.Store.ListProjects(ctx)
	if err != nil {
		return ProjectView{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if len(list) == 1 {
			return a.projectView(ctx, list[0])
		}
		return ProjectView{}, invalid("there are several projects; pass project (one of: %s)", projectSlugs(list))
	}
	id, _ := strconv.ParseInt(ref, 10, 64)
	for _, p := range list {
		if p.Slug == ref || p.Slug == slugify(ref) || strings.EqualFold(p.Name, ref) || p.ID == id {
			return a.projectView(ctx, p)
		}
	}
	return ProjectView{}, notFound("unknown project %q (available: %s)", ref, projectSlugs(list))
}

func projectSlugs(list []store.Project) string {
	var s []string
	for _, p := range list {
		s = append(s, p.Slug)
	}
	return strings.Join(s, ", ")
}

type ProjectInput struct {
	Name     string           `json:"name"`
	Slug     string           `json:"slug"`     // empty: derived from the name
	Settings *ProjectSettings `json:"settings"` // nil: defaults (create) or unchanged (update)
}

func (a *App) CreateProject(ctx context.Context, in ProjectInput) (ProjectView, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return ProjectView{}, invalid("project name is required")
	}
	s := ProjectSettings{DefaultAspect: string(media.StrategyBlur), DefaultPlatforms: a.platformIDs(), Brand: Brand{GameName: name}}
	if in.Settings != nil {
		s = *in.Settings
	}
	if err := a.checkProjectSettings(&s); err != nil {
		return ProjectView{}, err
	}
	slug := slugify(in.Slug)
	if slug == "" {
		var err error
		if slug, err = a.freeSlug(ctx, slugify(name)); err != nil {
			return ProjectView{}, err
		}
	}
	p := store.Project{Slug: slug, Name: name, Settings: mustMarshal(s)}
	if err := a.Store.CreateProject(ctx, &p); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return ProjectView{}, conflict("a project with the short name %q already exists", slug)
		}
		return ProjectView{}, err
	}
	return a.projectView(ctx, p)
}

func (a *App) UpdateProject(ctx context.Context, id int64, in ProjectInput) (ProjectView, error) {
	p, err := a.project(ctx, id)
	if err != nil {
		return ProjectView{}, err
	}
	if name := strings.TrimSpace(in.Name); name != "" {
		p.Name = name
	}
	if in.Slug != "" {
		if p.Slug = slugify(in.Slug); p.Slug == "" {
			return ProjectView{}, invalid("short name must contain letters or digits")
		}
	}
	if in.Settings != nil {
		s := *in.Settings
		if err := a.checkProjectSettings(&s); err != nil {
			return ProjectView{}, err
		}
		p.Settings = mustMarshal(s)
	}
	if err := a.Store.UpdateProject(ctx, &p); err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			return ProjectView{}, conflict("a project with the short name %q already exists", p.Slug)
		}
		return ProjectView{}, err
	}
	return a.projectView(ctx, p)
}

// DeleteProject removes an empty project and its account connections.
func (a *App) DeleteProject(ctx context.Context, id int64) error {
	if _, err := a.project(ctx, id); err != nil {
		return err
	}
	list, err := a.Store.ListProjects(ctx)
	if err != nil {
		return err
	}
	if len(list) <= 1 {
		return conflict("the last project can't be deleted")
	}
	n, err := a.Store.CountPosts(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return conflict("the project still has %d posts; delete them first", n)
	}
	return a.Store.DeleteProject(ctx, id)
}

func (a *App) checkProjectSettings(s *ProjectSettings) error {
	if s.DefaultAspect == "" {
		s.DefaultAspect = string(media.StrategyBlur)
	}
	if !media.Strategy(s.DefaultAspect).Valid() {
		return invalid("defaultAspect must be blur, crop or pad")
	}
	for _, p := range s.DefaultPlatforms {
		if _, ok := a.Registry.Get(platform.ID(p)); !ok {
			return invalid("unknown platform %q", p)
		}
	}
	s.Brand.GameName = strings.TrimSpace(s.Brand.GameName)
	s.Brand.Link = strings.TrimSpace(s.Brand.Link)
	s.Brand.DefaultHashtags = platform.NormalizeHashtags(s.Brand.DefaultHashtags)
	if s.DefaultPlatforms == nil {
		s.DefaultPlatforms = []string{}
	}
	slices.Sort(s.DefaultPlatforms)
	s.DefaultPlatforms = slices.Compact(s.DefaultPlatforms)
	return nil
}

// freeSlug returns base, or base-2, base-3… if it is taken.
func (a *App) freeSlug(ctx context.Context, base string) (string, error) {
	if base == "" {
		base = "project"
	}
	for i := 1; ; i++ {
		slug := base
		if i > 1 {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		_, err := a.Store.GetProjectBySlug(ctx, slug)
		if errors.Is(err, store.ErrNotFound) {
			return slug, nil
		}
		if err != nil {
			return "", err
		}
	}
}

var umlauts = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")

// slugify turns a name into a short ASCII id like "donnervolk" or "my-game".
func slugify(s string) string {
	s = umlauts.Replace(strings.ToLower(strings.TrimSpace(s)))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
