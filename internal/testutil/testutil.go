// Package testutil builds a real App (SQLite, ffmpeg, job queue) with fake
// platforms for end-to-end tests.
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"powerposter/internal/app"
	"powerposter/internal/config"
	"powerposter/internal/media"
	"powerposter/internal/platform"
	"powerposter/internal/secret"
	"powerposter/internal/store"
)

// HostInbox is how the inbox looks from the (simulated) host, like a Docker bind mount.
const HostInbox = `X:\host\inbox`

// FakeAdapter records publishes instead of calling a real platform.
type FakeAdapter struct {
	Name string

	mu        sync.Mutex
	Published []platform.ResolvedTarget
	Fail      error
	deleted   bool
}

func (f *FakeAdapter) ID() platform.ID     { return platform.ID(f.Name) }
func (f *FakeAdapter) DisplayName() string { return "Fake " + f.Name }
func (f *FakeAdapter) SupportedKinds() []platform.PostKind {
	return []platform.PostKind{platform.KindVideo}
}
func (f *FakeAdapter) Configured() bool             { return true }
func (f *FakeAdapter) Auth() platform.Authenticator { return fakeAuth{} }
func (f *FakeAdapter) OptionsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"flag":{"type":"boolean","title":"Flag","default":false}}}`)
}
func (f *FakeAdapter) DefaultOptions() json.RawMessage                             { return json.RawMessage(`{"flag":false}`) }
func (f *FakeAdapter) Validate(platform.ResolvedTarget) []platform.ValidationIssue { return nil }
func (f *FakeAdapter) ReviewNotices(json.RawMessage) []string                      { return nil }

func (f *FakeAdapter) Constraints(platform.PostKind) platform.Constraints {
	return platform.Constraints{
		Video: &media.VideoConstraints{
			Containers: []string{"mp4", "mov"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"},
			MaxWidth: 1080, MaxHeight: 1920, TargetWidth: 1080, TargetHeight: 1920,
			FPSMin: 23, FPSMax: 60, DurationMin: 1, DurationMax: 60, MaxBytes: 100 << 20,
		},
		Caption: platform.CaptionRules{MaxChars: 100, MaxHashtags: 5},
	}
}

func (f *FakeAdapter) Publish(ctx context.Context, pc platform.PublishContext, t platform.ResolvedTarget) (platform.PublishResult, error) {
	pc.Progress("uploading")
	if pc.Token.AccessToken != "fake-token" {
		return platform.PublishResult{}, fmt.Errorf("wrong token %q", pc.Token.AccessToken)
	}
	if _, err := os.Stat(t.FilePath); err != nil {
		return platform.PublishResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Fail != nil {
		return platform.PublishResult{}, f.Fail
	}
	f.Published = append(f.Published, t)
	return platform.PublishResult{RemoteID: "remote-1", Permalink: "https://example.com/" + f.Name + "/1"}, nil
}

func (f *FakeAdapter) SetFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Fail = err
}

func (f *FakeAdapter) PublishedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Published)
}

type fakeAuth struct{}

func (fakeAuth) AuthorizeURL(state, redirectURI, verifier string) string {
	return "https://fake/auth?state=" + state
}
func (fakeAuth) Exchange(context.Context, string, string, string) (platform.TokenSet, error) {
	return platform.TokenSet{AccessToken: "fake-token", AccountName: "Fake Account", ExpiresAt: time.Now().Add(24 * time.Hour)}, nil
}
func (fakeAuth) Refresh(_ context.Context, t platform.TokenSet) (platform.TokenSet, error) {
	return t, nil
}
func (fakeAuth) RefreshMargin() time.Duration { return time.Minute }

// RequireFFmpeg skips the test when ffmpeg isn't installed.
func RequireFFmpeg(t *testing.T) media.Runner {
	t.Helper()
	r := media.Runner{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
	if r.Available() != nil {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	return r
}

// NewApp starts an App with the given fake platforms, all connected.
func NewApp(t *testing.T, fakes ...*FakeAdapter) *app.App {
	t.Helper()
	runner := RequireFFmpeg(t)
	dir := t.TempDir()
	cfg := config.Config{
		DataDir:            filepath.Join(dir, "data"),
		InboxDir:           filepath.Join(dir, "inbox"),
		InboxHostPath:      HostInbox,
		BaseURL:            "http://127.0.0.1:4000",
		PublicBaseURL:      "https://pp.example.com",
		FFmpegEncoder:      "libx264",
		MaxParallelEncodes: 2,
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.Open(secret.NewKey())
	if err != nil {
		t.Fatal(err)
	}
	var adapters []platform.Adapter
	for _, f := range fakes {
		adapters = append(adapters, f)
	}
	a := app.New(cfg, st, platform.NewRegistry(adapters...), runner, box, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	if err := a.Init(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		st.Close()
	})
	for _, f := range fakes {
		Connect(t, a, f.Name)
	}
	return a
}

// Project is the project every new database starts with.
const Project int64 = 1

// Connect runs the OAuth flow against a fake platform for the first project.
func Connect(t *testing.T, a *app.App, platformID string) {
	t.Helper()
	ConnectProject(t, a, Project, platformID)
}

// ConnectProject runs the OAuth flow against a fake platform for a project.
func ConnectProject(t *testing.T, a *app.App, projectID int64, platformID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := a.StartConnect(ctx, projectID, platformID); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf("state-%d-%s", projectID, platformID)
	if err := a.Store.SaveOAuthState(ctx, state, store.OAuthState{ProjectID: projectID, Platform: platformID, Verifier: "verifier"}); err != nil {
		t.Fatal(err)
	}
	if pid, err := a.FinishConnect(ctx, platformID, "code", state); err != nil || pid != projectID {
		t.Fatalf("finish connect: project %d, %v", pid, err)
	}
}

// MakeVideo writes a test clip into the app's inbox and returns its host-side path.
func MakeVideo(t *testing.T, a *app.App, name string, w, h, fps int, seconds float64) string {
	t.Helper()
	out := filepath.Join(a.Cfg.InboxDir, name)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", w, h, fps),
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", fmt.Sprint(seconds), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make video: %v %s", err, b)
	}
	return HostInbox + `\` + name
}

// WaitFor polls cond until it returns true or the timeout passes.
func WaitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitReady waits until all renditions of a post are converted.
func WaitReady(t *testing.T, a *app.App, id int64) app.PostView {
	t.Helper()
	var v app.PostView
	WaitFor(t, "renditions", 60*time.Second, func() bool {
		var err error
		v, err = a.GetPost(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, tg := range v.Targets {
			if tg.Enabled && (tg.Rendition == nil || tg.Rendition.Status == "pending" || tg.Rendition.Status == "processing") {
				return false
			}
		}
		return true
	})
	return v
}

// IsLive implements platform.LiveChecker; SetDeleted simulates deleting the post in the app.
func (f *FakeAdapter) IsLive(context.Context, platform.TokenSet, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.deleted, nil
}

func (f *FakeAdapter) SetDeleted(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = v
}

// MakeImage writes a test PNG into the app's inbox and returns its local path.
func MakeImage(t *testing.T, a *app.App, name string) string {
	t.Helper()
	out := filepath.Join(a.Cfg.InboxDir, name)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=1280x720", "-frames:v", "1", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make image: %v %s", err, b)
	}
	return out
}
