package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"powerposter/internal/app"
	"powerposter/internal/platform"
	"powerposter/internal/testutil"
)

func ptr[T any](v T) *T { return &v }

func TestDraftReviewPublishFlow(t *testing.T) {
	fake := &testutil.FakeAdapter{Name: "fake"}
	a := testutil.NewApp(t, fake)
	ctx := context.Background()

	v, err := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project, Title: "Launch", Caption: "Raid the longhouse!", Hashtags: ptr([]string{"#indiegame", "gamedev"})})
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != app.StatusDraft || len(v.Targets) != 1 || !v.Targets[0].Enabled {
		t.Fatalf("unexpected new post %+v", v)
	}
	if got := v.Targets[0].FinalCaption; got != "Raid the longhouse!\n\n#indiegame #gamedev" {
		t.Fatalf("final caption %q", got)
	}

	// Can't approve without a video.
	if _, err := a.Approve(ctx, v.ID); app.KindOf(err) != app.KindConflict {
		t.Fatalf("approve without media: %v", err)
	}

	// Attach a landscape 120 fps clip through the host-path mapping (as Claude would).
	hostPath := testutil.MakeVideo(t, a, "clip.mp4", 640, 360, 120, 2)
	local, err := a.ResolveInboxPath(hostPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AttachFile(ctx, v.ID, local, "", false); err != nil {
		t.Fatal(err)
	}
	v = testutil.WaitReady(t, a, v.ID)
	r := v.Targets[0].Rendition
	if r.Status != "ready" || r.Info.Width != 1080 || r.Info.Height != 1920 || r.Info.FPS != 60 {
		t.Fatalf("rendition %+v info %+v", r, r.Info)
	}
	if !v.Ready || v.CanPublish {
		t.Fatalf("expected ready but not publishable before approval: ready=%v canPublish=%v issues=%v", v.Ready, v.CanPublish, v.Issues)
	}

	// Approve, then edit: approval is dropped.
	if v, err = a.Approve(ctx, v.ID); err != nil || v.Status != app.StatusApproved || !v.ApprovalValid || !v.CanPublish {
		t.Fatalf("approve: %v %+v", err, v.Post)
	}
	if v, err = a.UpdatePost(ctx, v.ID, app.PostPatch{Caption: ptr("Raid the longhouse! Wishlist now.")}); err != nil {
		t.Fatal(err)
	}
	if v.Status != app.StatusDraft || v.ApprovalValid {
		t.Fatalf("edit after approval should reset to draft: %s %v", v.Status, v.ApprovalValid)
	}
	// Title is internal: changing it keeps an approval.
	if v, err = a.Approve(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if v, err = a.UpdatePost(ctx, v.ID, app.PostPatch{Title: ptr("Launch v2")}); err != nil || v.Status != app.StatusApproved {
		t.Fatalf("title change dropped approval: %v %s", err, v.Status)
	}

	// Claude can't publish unless the user allowed it.
	if _, err := a.Publish(ctx, v.ID, nil, true); app.KindOf(err) != app.KindForbidden {
		t.Fatalf("MCP publish should be forbidden: %v", err)
	}

	if v, err = a.Publish(ctx, v.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "published", 10*time.Second, func() bool {
		v, _ = a.GetPost(ctx, v.ID)
		return v.Status == app.StatusPublished
	})
	tg := v.Targets[0]
	if tg.Status != "published" || tg.Permalink != "https://example.com/fake/1" || tg.PublishedAt == nil {
		t.Fatalf("target after publish %+v", tg.Target)
	}
	if fake.PublishedCount() != 1 || fake.Published[0].Caption != "Raid the longhouse! Wishlist now.\n\n#indiegame #gamedev" {
		t.Fatalf("published %+v", fake.Published)
	}

	// Published posts are locked.
	if _, err := a.UpdatePost(ctx, v.ID, app.PostPatch{Caption: ptr("x")}); app.KindOf(err) != app.KindConflict {
		t.Fatalf("edit after publish: %v", err)
	}
	attempts, err := a.Store.ListAttempts(ctx, v.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "succeeded" || !strings.Contains(attempts[0].Log, "uploading") {
		t.Fatalf("attempts %+v %v", attempts, err)
	}
}

func TestFailedPublishCanBeRetried(t *testing.T) {
	fake := &testutil.FakeAdapter{Name: "fake"}
	a := testutil.NewApp(t, fake)
	ctx := context.Background()

	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project, Caption: "Hi"})
	local, _ := a.ResolveInboxPath(testutil.MakeVideo(t, a, "v.mp4", 1080, 1920, 30, 1.5))
	if _, err := a.AttachFile(ctx, v.ID, local, "", false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitReady(t, a, v.ID)
	if _, err := a.Approve(ctx, v.ID); err != nil {
		t.Fatal(err)
	}

	fake.SetFail(errors.New("platform said no"))
	if _, err := a.Publish(ctx, v.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "failed", 10*time.Second, func() bool {
		v, _ = a.GetPost(ctx, v.ID)
		return v.Status == app.StatusFailed
	})
	if v.Targets[0].Error != "platform said no" || !v.CanPublish {
		t.Fatalf("after failure: %+v canPublish=%v", v.Targets[0].Target, v.CanPublish)
	}

	fake.SetFail(nil)
	if _, err := a.Publish(ctx, v.ID, []string{"fake"}, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "published", 10*time.Second, func() bool {
		v, _ = a.GetPost(ctx, v.ID)
		return v.Status == app.StatusPublished
	})
}

func TestIdenticalOutputsShareOneFile(t *testing.T) {
	a := testutil.NewApp(t, &testutil.FakeAdapter{Name: "one"}, &testutil.FakeAdapter{Name: "two"})
	ctx := context.Background()
	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project})
	local, _ := a.ResolveInboxPath(testutil.MakeVideo(t, a, "v.mp4", 640, 360, 30, 1.5))
	if _, err := a.AttachFile(ctx, v.ID, local, "", false); err != nil {
		t.Fatal(err)
	}
	v = testutil.WaitReady(t, a, v.ID)
	r1, r2 := v.Targets[0].Rendition, v.Targets[1].Rendition
	if r1.URL == "" || r1.URL != r2.URL || r1.Checksum != r2.Checksum {
		t.Fatalf("renditions not shared: %q %q", r1.URL, r2.URL)
	}

	// Switching the fill strategy re-renders.
	v, err := a.UpdatePost(ctx, v.ID, app.PostPatch{AspectStrategy: ptr("crop")})
	if err != nil {
		t.Fatal(err)
	}
	v = testutil.WaitReady(t, a, v.ID)
	if v.Targets[0].Rendition.URL == r1.URL {
		t.Fatal("expected a new rendition after changing the strategy")
	}
}

func TestValidationErrors(t *testing.T) {
	a := testutil.NewApp(t, &testutil.FakeAdapter{Name: "fake"})
	ctx := context.Background()
	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project, Caption: strings.Repeat("x", 120), Hashtags: ptr([]string{"a", "b", "c", "d", "e", "f"})})

	codes := map[string]bool{}
	for _, i := range v.Issues {
		codes[i.Code] = true
	}
	for _, want := range []string{"NO_MEDIA", "CAPTION_TOO_LONG", "TOO_MANY_HASHTAGS"} {
		if !codes[want] {
			t.Errorf("missing %s in %v", want, codes)
		}
	}

	// Video longer than the 60 s limit can't be fixed automatically.
	local, _ := a.ResolveInboxPath(testutil.MakeVideo(t, a, "long.mp4", 320, 240, 24, 61))
	if _, err := a.AttachFile(ctx, v.ID, local, "", false); err != nil {
		t.Fatal(err)
	}
	v, _ = a.GetPost(ctx, v.ID)
	found := false
	for _, i := range v.Issues {
		found = found || i.Code == "DURATION_TOO_LONG"
	}
	if !found {
		t.Fatalf("missing DURATION_TOO_LONG in %v", v.Issues)
	}

	// Unknown options and wrong types are rejected with a clear message.
	_, err := a.UpdateTarget(ctx, v.ID, "fake", app.TargetPatch{Options: json.RawMessage(`{"nope":true}`)})
	if app.KindOf(err) != app.KindInvalid || !strings.Contains(err.Error(), "unknown option") {
		t.Fatalf("unknown option: %v", err)
	}
	_, err = a.UpdateTarget(ctx, v.ID, "fake", app.TargetPatch{Options: json.RawMessage(`{"flag":"yes"}`)})
	if app.KindOf(err) != app.KindInvalid {
		t.Fatalf("wrong type: %v", err)
	}
	if v, err = a.UpdateTarget(ctx, v.ID, "fake", app.TargetPatch{Options: json.RawMessage(`{"flag":true}`)}); err != nil || string(v.Targets[0].Options) != `{"flag":true}` {
		t.Fatalf("valid option: %v %s", err, v.Targets[0].Options)
	}
	// Not a video.
	if _, err := a.AttachUpload(ctx, v.ID, strings.NewReader("hello"), "notes.txt"); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("non-video upload: %v", err)
	}
}

func TestResolveInboxPath(t *testing.T) {
	a := testutil.NewApp(t)
	testutil.MakeVideo(t, a, "ok.mp4", 160, 120, 24, 0.5)
	for _, ok := range []string{`X:\host\inbox\ok.mp4`, `x:/HOST/inbox/ok.mp4`, a.Cfg.InboxDir + "/ok.mp4"} {
		if _, err := a.ResolveInboxPath(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{`C:\Users\me\video.mp4`, `X:\host\inbox\..\secret.mp4`, `X:\host\inbox\missing.mp4`, `X:\host\inboxer\ok.mp4`} {
		if _, err := a.ResolveInboxPath(bad); app.KindOf(err) != app.KindInvalid {
			t.Errorf("%s: expected invalid, got %v", bad, err)
		}
	}
}

func TestProjectSettings(t *testing.T) {
	a := testutil.NewApp(t, &testutil.FakeAdapter{Name: "fake"})
	ctx := context.Background()
	p, err := a.GetProject(ctx, testutil.Project)
	if err != nil || p.Settings.DefaultAspect != "blur" || len(p.Settings.DefaultPlatforms) != 1 {
		t.Fatalf("defaults %+v %v", p, err)
	}
	s := p.Settings
	s.Brand.DefaultHashtags = []string{"#Donnervolk", "donnervolk", "indiegame"}
	if p, err = a.UpdateProject(ctx, p.ID, app.ProjectInput{Settings: &s}); err != nil || len(p.Settings.Brand.DefaultHashtags) != 2 {
		t.Fatalf("save %+v %v", p, err)
	}
	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project})
	if strings.Join(v.Hashtags, ",") != "Donnervolk,indiegame" {
		t.Fatalf("brand hashtags not applied: %v", v.Hashtags)
	}
	s.DefaultPlatforms = []string{"nope"}
	if _, err := a.UpdateProject(ctx, p.ID, app.ProjectInput{Settings: &s}); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("unknown platform accepted: %v", err)
	}
}

func TestProjectsAreSeparate(t *testing.T) {
	fake := &testutil.FakeAdapter{Name: "fake"}
	a := testutil.NewApp(t, fake)
	ctx := context.Background()

	other, err := a.CreateProject(ctx, app.ProjectInput{Name: "Über Space Game"})
	if err != nil || other.Slug != "ueber-space-game" || other.Settings.Brand.GameName != "Über Space Game" || len(other.Settings.DefaultPlatforms) != 1 {
		t.Fatalf("create %+v %v", other, err)
	}
	if dup, err := a.CreateProject(ctx, app.ProjectInput{Name: "Über Space Game"}); err != nil || dup.Slug != "ueber-space-game-2" {
		t.Fatalf("second project with same name: %+v %v", dup, err)
	}
	if _, err := a.CreateProject(ctx, app.ProjectInput{Name: " "}); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("empty name accepted: %v", err)
	}
	for _, ref := range []string{"ueber-space-game", "Über Space Game", "UEBER-SPACE-GAME"} {
		if p, err := a.ResolveProject(ctx, ref); err != nil || p.ID != other.ID {
			t.Fatalf("resolve %q: %+v %v", ref, p, err)
		}
	}
	if _, err := a.ResolveProject(ctx, ""); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("empty ref with several projects: %v", err)
	}

	// The new project has its own (missing) connection.
	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: other.ID, Caption: "Hi"})
	if v.Project.Slug != "ueber-space-game" || !hasIssue(v, "NOT_CONNECTED") {
		t.Fatalf("expected NOT_CONNECTED in other project: %+v", v.Issues)
	}
	testutil.ConnectProject(t, a, other.ID, "fake")
	if v, _ = a.GetPost(ctx, v.ID); hasIssue(v, "NOT_CONNECTED") {
		t.Fatal("still not connected after connecting the project")
	}
	if err := a.Disconnect(ctx, other.ID, "fake"); err != nil {
		t.Fatal(err)
	}
	mine, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project, Caption: "Hi"})
	if hasIssue(mine, "NOT_CONNECTED") {
		t.Fatal("disconnecting one project disconnected the other")
	}

	list, _ := a.ListPosts(ctx, other.ID, "", "")
	if len(list) != 1 || list[0].ID != v.ID {
		t.Fatalf("posts of other project: %+v", list)
	}
	if dup, err := a.DuplicatePost(ctx, v.ID); err != nil || dup.ProjectID != other.ID {
		t.Fatalf("duplicate keeps project: %+v %v", dup.Post, err)
	}

	if err := a.DeleteProject(ctx, other.ID); app.KindOf(err) != app.KindConflict {
		t.Fatalf("delete project with posts: %v", err)
	}
	if err := a.DeleteProject(ctx, other.ID+1); err != nil {
		t.Fatalf("delete empty project: %v", err)
	}
	if _, err := a.CreatePost(ctx, app.CreatePostInput{}); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("post without project: %v", err)
	}
}

func hasIssue(v app.PostView, code string) bool {
	for _, i := range v.Issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

// localFake is a platform that accepts http://localhost OAuth redirects (like TikTok).
type localFake struct{ *testutil.FakeAdapter }

func (localFake) AllowsLocalRedirect() bool { return true }

func TestLocalRedirectWithoutTunnel(t *testing.T) {
	a := testutil.NewApp(t, &testutil.FakeAdapter{Name: "remote"})
	a.Registry = platform.NewRegistry(&testutil.FakeAdapter{Name: "remote"}, localFake{&testutil.FakeAdapter{Name: "local"}})
	a.Cfg.PublicBaseURL = ""
	ctx := context.Background()

	list, err := a.Platforms(ctx, testutil.Project)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]app.PlatformView{}
	for _, p := range list {
		byID[p.ID] = p
	}
	if p := byID["local"]; p.RedirectURI != "http://localhost:4000/oauth/local/callback" || len(p.MissingSettings) != 0 {
		t.Fatalf("local platform: %q missing %v", p.RedirectURI, p.MissingSettings)
	}
	if p := byID["remote"]; p.RedirectURI != "" || strings.Join(p.MissingSettings, ",") != "PUBLIC_BASE_URL" {
		t.Fatalf("remote platform: %q missing %v", p.RedirectURI, p.MissingSettings)
	}
	if _, err := a.StartConnect(ctx, testutil.Project, "local"); err != nil {
		t.Fatalf("local connect: %v", err)
	}
	if _, err := a.StartConnect(ctx, testutil.Project, "remote"); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("remote connect without tunnel: %v", err)
	}
}

func TestPublishPlatformsIndividually(t *testing.T) {
	ig, tt := &testutil.FakeAdapter{Name: "ig"}, &testutil.FakeAdapter{Name: "tt"}
	a := testutil.NewApp(t, ig, tt)
	ctx := context.Background()
	if err := a.Disconnect(ctx, testutil.Project, "tt"); err != nil { // tt can't publish yet
		t.Fatal(err)
	}

	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project, Caption: "Hi"})
	local, _ := a.ResolveInboxPath(testutil.MakeVideo(t, a, "v.mp4", 1080, 1920, 30, 1.5))
	if _, err := a.AttachFile(ctx, v.ID, local, "", false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitReady(t, a, v.ID)
	v, err := a.Approve(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.CanPublish || v.CanPublishAll || !v.Targets[0].CanPublish || v.Targets[1].CanPublish {
		t.Fatalf("canPublish=%v all=%v ig=%v tt=%v", v.CanPublish, v.CanPublishAll, v.Targets[0].CanPublish, v.Targets[1].CanPublish)
	}

	// "Publish all" is refused and names the blocked platform...
	if _, err := a.Publish(ctx, v.ID, nil, false); app.KindOf(err) != app.KindConflict || !strings.Contains(err.Error(), "Fake tt") {
		t.Fatalf("publish all: %v", err)
	}
	// ...but the connected platform can go out on its own.
	if _, err := a.Publish(ctx, v.ID, []string{"ig"}, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "partial", 10*time.Second, func() bool {
		v, _ = a.GetPost(ctx, v.ID)
		return v.Status == app.StatusPartial
	})
	if ig.PublishedCount() != 1 || tt.PublishedCount() != 0 || v.Targets[1].Status != "idle" {
		t.Fatalf("ig=%d tt=%d ttStatus=%s", ig.PublishedCount(), tt.PublishedCount(), v.Targets[1].Status)
	}

	// Later: connect tt and publish the rest.
	testutil.Connect(t, a, "tt")
	if _, err := a.Publish(ctx, v.ID, []string{"tt"}, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "published", 10*time.Second, func() bool {
		v, _ = a.GetPost(ctx, v.ID)
		return v.Status == app.StatusPublished
	})
	if _, err := a.Publish(ctx, v.ID, []string{"nope"}, false); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("unknown platform: %v", err)
	}
}

func TestCoverImageAndFrame(t *testing.T) {
	a := testutil.NewApp(t, &testutil.FakeAdapter{Name: "fake"})
	ctx := context.Background()
	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project, Caption: "Hi"})
	local, _ := a.ResolveInboxPath(testutil.MakeVideo(t, a, "v.mp4", 1080, 1920, 30, 2))
	if _, err := a.AttachFile(ctx, v.ID, local, "", false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitReady(t, a, v.ID)
	v, err := a.UpdatePost(ctx, v.ID, app.PostPatch{CoverMs: ptr(int64(1200))})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.CoverURL, "-f1200-") {
		t.Fatalf("frame cover url %q", v.CoverURL)
	}
	if p, err := a.CoverFile(ctx, strings.TrimPrefix(v.CoverURL, "/media/covers/")); err != nil {
		t.Fatalf("render frame: %v", err)
	} else if st, err := os.Stat(p); err != nil || st.Size() == 0 {
		t.Fatalf("frame file: %v", err)
	}

	if v, err = a.Approve(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	img, err := os.Open(testutil.MakeImage(t, a, "cover.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	if v, err = a.SetCoverImage(ctx, v.ID, img, "cover.png"); err != nil {
		t.Fatal(err)
	}
	if !v.CoverCustom || !strings.Contains(v.CoverURL, "-c") || v.ApprovalValid {
		t.Fatalf("custom cover: custom=%v url=%q approvalValid=%v", v.CoverCustom, v.CoverURL, v.ApprovalValid)
	}
	if u, ok := a.PublicMediaURL(v.CoverPath, time.Hour); !ok {
		t.Fatal("no public link")
	} else if p, ok := a.ResolvePublicMedia(u[strings.LastIndex(u, "/")+1:]); !ok || p != v.CoverPath {
		t.Fatalf("public cover link resolves to %q", p)
	}
	if v, err = a.ClearCoverImage(ctx, v.ID); err != nil || v.CoverCustom || !strings.Contains(v.CoverURL, "-f1200-") {
		t.Fatalf("clear: %v custom=%v url=%q", err, v.CoverCustom, v.CoverURL)
	}
	if _, err := a.SetCoverImage(ctx, v.ID, strings.NewReader("not an image"), "x.png"); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("bad image: %v", err)
	}
}

func TestDeletedPostCanBeRepublished(t *testing.T) {
	fake := &testutil.FakeAdapter{Name: "fake"}
	a := testutil.NewApp(t, fake)
	ctx := context.Background()
	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project, Caption: "Hi"})
	local, _ := a.ResolveInboxPath(testutil.MakeVideo(t, a, "v.mp4", 1080, 1920, 30, 1.5))
	if _, err := a.AttachFile(ctx, v.ID, local, "", false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitReady(t, a, v.ID)
	if _, err := a.Approve(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Publish(ctx, v.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "published", 10*time.Second, func() bool {
		v, _ = a.GetPost(ctx, v.ID)
		return v.Status == app.StatusPublished
	})

	// Still online: nothing changes.
	if v, _ = a.CheckLive(ctx, v.ID); v.Status != app.StatusPublished {
		t.Fatalf("still live: %s", v.Status)
	}

	fake.SetDeleted(true)
	v, err := a.CheckLive(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Targets[0].Status != "removed" || v.Status != app.StatusApproved || !v.CanPublish {
		t.Fatalf("after delete: target=%s post=%s canPublish=%v", v.Targets[0].Status, v.Status, v.CanPublish)
	}
	// The post is editable again (e.g. to fix the cover); that drops the approval.
	if v, err = a.UpdatePost(ctx, v.ID, app.PostPatch{CoverMs: ptr(int64(500))}); err != nil || v.Status != app.StatusDraft {
		t.Fatalf("edit after removal: %v %s", err, v.Status)
	}
	if _, err := a.Approve(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	fake.SetDeleted(false)
	if _, err := a.Publish(ctx, v.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "republished", 10*time.Second, func() bool {
		v, _ = a.GetPost(ctx, v.ID)
		return v.Status == app.StatusPublished
	})
	if fake.PublishedCount() != 2 || fake.Published[1].CoverMs != 500 {
		t.Fatalf("published %d times, cover %d", fake.PublishedCount(), fake.Published[len(fake.Published)-1].CoverMs)
	}
}

func TestDuplicatePartlyPublishedPost(t *testing.T) {
	ig, tt := &testutil.FakeAdapter{Name: "ig"}, &testutil.FakeAdapter{Name: "tt"}
	a := testutil.NewApp(t, ig, tt)
	ctx := context.Background()
	v, _ := a.CreatePost(ctx, app.CreatePostInput{ProjectID: testutil.Project, Title: "Mood video", Caption: "Hi", Hashtags: ptr([]string{"indiegame"})})
	local, _ := a.ResolveInboxPath(testutil.MakeVideo(t, a, "v.mp4", 640, 360, 30, 1.5))
	if _, err := a.AttachFile(ctx, v.ID, local, "", false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitReady(t, a, v.ID)
	if _, err := a.UpdateTarget(ctx, v.ID, "ig", app.TargetPatch{CaptionOverride: ptr("IG text"), Options: json.RawMessage(`{"flag":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Approve(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Publish(ctx, v.ID, []string{"tt"}, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "tt published", 10*time.Second, func() bool {
		v, _ = a.GetPost(ctx, v.ID)
		return v.Status == app.StatusPartial
	})

	c, err := a.DuplicatePost(ctx, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == v.ID || c.Title != "Mood video (copy)" || c.Status != app.StatusDraft || c.Caption != "Hi" {
		t.Fatalf("copy: %+v", c.Post)
	}
	byName := map[string]app.TargetView{}
	for _, tg := range c.Targets {
		byName[tg.Platform] = tg
	}
	if !byName["ig"].Enabled || byName["tt"].Enabled {
		t.Fatalf("only the not-yet-live platform should be enabled: ig=%v tt=%v", byName["ig"].Enabled, byName["tt"].Enabled)
	}
	if byName["ig"].FinalCaption != "IG text\n\n#indiegame" || string(byName["ig"].Options) != `{"flag":true}` {
		t.Fatalf("overrides not copied: %q %s", byName["ig"].FinalCaption, byName["ig"].Options)
	}
	// Conversions are copied, not redone: ready immediately, own file.
	r := byName["ig"].Rendition
	if r == nil || r.Status != "ready" || r.URL == v.Targets[0].Rendition.URL || r.Checksum != v.Targets[0].Rendition.Checksum {
		t.Fatalf("rendition not copied: %+v", r)
	}
	if !c.Ready {
		t.Fatalf("copy should be ready to approve: %v", c.Issues)
	}

	// Deleting the original keeps the copy's files.
	if err := a.DeletePost(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Approve(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Publish(ctx, c.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, "copy published", 10*time.Second, func() bool {
		c, _ = a.GetPost(ctx, c.ID)
		return c.Status == app.StatusPublished
	})
	if ig.PublishedCount() != 1 || tt.PublishedCount() != 1 {
		t.Fatalf("ig=%d tt=%d", ig.PublishedCount(), tt.PublishedCount())
	}
}
