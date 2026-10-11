// Package app holds PowerPoster's use cases. The REST API and the MCP server
// are thin layers over it, so both get the same validation and review rules.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"powerposter/internal/config"
	"powerposter/internal/jobs"
	"powerposter/internal/media"
	"powerposter/internal/platform"
	"powerposter/internal/secret"
	"powerposter/internal/store"
)

const (
	jobTranscode = "transcode"
	jobPublish   = "publish"
)

type App struct {
	Cfg      config.Config
	Store    *store.Store
	Registry *platform.Registry
	Media    media.Runner
	Box      *secret.Box
	Queue    *jobs.Queue
	Log      *slog.Logger

	// postMu serializes post mutations from the UI and MCP.
	postMu sync.Mutex
	// renditionMu guards rendition status reads/writes between edits and transcode jobs.
	renditionMu sync.Mutex
}

func New(cfg config.Config, st *store.Store, reg *platform.Registry, runner media.Runner, box *secret.Box, log *slog.Logger) *App {
	a := &App{Cfg: cfg, Store: st, Registry: reg, Media: runner, Box: box, Log: log}
	a.Queue = jobs.New(st, log)
	a.Queue.Register(jobTranscode, cfg.MaxParallelEncodes, a.runTranscode)
	a.Queue.Register(jobPublish, 2, a.runPublish)
	return a
}

// Init creates folders and recovers work interrupted by a previous shutdown.
func (a *App) Init(ctx context.Context) error {
	for _, d := range []string{a.originalsDir(), a.renditionsDir(), a.thumbsDir(), a.Cfg.InboxDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	failed, err := a.Store.RecoverJobs(ctx, jobTranscode)
	if err != nil {
		return err
	}
	for _, j := range failed {
		if j.Type == jobPublish {
			a.recoverPublish(ctx, j.Payload)
		}
	}
	return nil
}

// Run starts background work and blocks until ctx is cancelled.
func (a *App) Run(ctx context.Context) {
	go a.refreshLoop(ctx)
	a.Queue.Run(ctx)
}

func (a *App) originalsDir() string  { return filepath.Join(a.Cfg.DataDir, "media", "originals") }
func (a *App) renditionsDir() string { return filepath.Join(a.Cfg.DataDir, "media", "renditions") }
func (a *App) thumbsDir() string     { return filepath.Join(a.Cfg.DataDir, "media", "thumbs") }

// MediaFile maps a public media URL segment to a file on disk.
func (a *App) MediaFile(kind, name string) (string, bool) {
	name = filepath.Base(name)
	switch kind {
	case "originals":
		return filepath.Join(a.originalsDir(), name), true
	case "renditions":
		return filepath.Join(a.renditionsDir(), name), true
	case "thumbs":
		return filepath.Join(a.thumbsDir(), name), true
	}
	return "", false
}

func mediaURL(kind, path string) string {
	if path == "" {
		return ""
	}
	return "/media/" + kind + "/" + filepath.Base(path)
}

// ---- Errors ----

type ErrorKind int

const (
	KindInvalid ErrorKind = iota + 1
	KindNotFound
	KindConflict
	KindForbidden
)

// Error is a user-facing error with a category the API maps to an HTTP status.
type Error struct {
	Kind ErrorKind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &Error{KindInvalid, fmt.Sprintf(format, args...)}
}
func conflict(format string, args ...any) error {
	return &Error{KindConflict, fmt.Sprintf(format, args...)}
}
func forbidden(format string, args ...any) error {
	return &Error{KindForbidden, fmt.Sprintf(format, args...)}
}
func notFound(format string, args ...any) error {
	return &Error{KindNotFound, fmt.Sprintf(format, args...)}
}

// KindOf returns the category of err (0 for internal errors).
func KindOf(err error) ErrorKind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if errors.Is(err, store.ErrNotFound) {
		return KindNotFound
	}
	return 0
}

// ---- Settings ----

type Brand struct {
	GameName        string   `json:"gameName"`
	Link            string   `json:"link"`
	DefaultHashtags []string `json:"defaultHashtags"`
	ToneNotes       string   `json:"toneNotes"`
}

type Settings struct {
	Brand            Brand    `json:"brand"`
	DefaultPlatforms []string `json:"defaultPlatforms"`
	DefaultAspect    string   `json:"defaultAspect"`
	MCPAllowPublish  bool     `json:"mcpAllowPublish"`
}

const settingsKey = "settings"

func (a *App) GetSettings(ctx context.Context) (Settings, error) {
	s := Settings{DefaultAspect: string(media.StrategyBlur)}
	for _, ad := range a.Registry.All() {
		s.DefaultPlatforms = append(s.DefaultPlatforms, string(ad.ID()))
	}
	if _, err := a.Store.GetSetting(ctx, settingsKey, &s); err != nil {
		return s, err
	}
	if s.Brand.DefaultHashtags == nil {
		s.Brand.DefaultHashtags = []string{}
	}
	return s, nil
}

func (a *App) SaveSettings(ctx context.Context, s Settings) (Settings, error) {
	if !media.Strategy(s.DefaultAspect).Valid() {
		return s, invalid("defaultAspect must be blur, crop or pad")
	}
	for _, p := range s.DefaultPlatforms {
		if _, ok := a.Registry.Get(platform.ID(p)); !ok {
			return s, invalid("unknown platform %q", p)
		}
	}
	s.Brand.DefaultHashtags = platform.NormalizeHashtags(s.Brand.DefaultHashtags)
	s.DefaultPlatforms = slices.Compact(s.DefaultPlatforms)
	return s, a.Store.SetSetting(ctx, settingsKey, s)
}

// ---- Status ----

type SystemStatus struct {
	FFmpeg        string    `json:"ffmpeg"` // "ok" or an error
	Encoder       string    `json:"encoder"`
	InboxHostPath string    `json:"inboxHostPath"`
	MCPURL        string    `json:"mcpUrl"`
	PublicBaseURL string    `json:"publicBaseUrl"`
	Time          time.Time `json:"time"`
}

func (a *App) Status() SystemStatus {
	ff := "ok"
	if err := a.Media.Available(); err != nil {
		ff = err.Error()
	}
	return SystemStatus{
		FFmpeg:        ff,
		Encoder:       a.Cfg.FFmpegEncoder,
		InboxHostPath: a.Cfg.InboxHostPath,
		MCPURL:        a.Cfg.BaseURL + "/mcp",
		PublicBaseURL: a.Cfg.PublicBaseURL,
		Time:          time.Now(),
	}
}
