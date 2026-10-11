package app_test

import (
	"context"
	"encoding/json"
	"errors"
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

	v, err := a.CreatePost(ctx, app.CreatePostInput{Title: "Launch", Caption: "Raid the longhouse!", Hashtags: ptr([]string{"#indiegame", "gamedev"})})
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

	v, _ := a.CreatePost(ctx, app.CreatePostInput{Caption: "Hi"})
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
	v, _ := a.CreatePost(ctx, app.CreatePostInput{})
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
	v, _ := a.CreatePost(ctx, app.CreatePostInput{Caption: strings.Repeat("x", 120), Hashtags: ptr([]string{"a", "b", "c", "d", "e", "f"})})

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

func TestSettings(t *testing.T) {
	a := testutil.NewApp(t, &testutil.FakeAdapter{Name: "fake"})
	ctx := context.Background()
	s, err := a.GetSettings(ctx)
	if err != nil || s.DefaultAspect != "blur" || len(s.DefaultPlatforms) != 1 {
		t.Fatalf("defaults %+v %v", s, err)
	}
	s.Brand.DefaultHashtags = []string{"#Donnervolk", "donnervolk", "indiegame"}
	if s, err = a.SaveSettings(ctx, s); err != nil || len(s.Brand.DefaultHashtags) != 2 {
		t.Fatalf("save %+v %v", s, err)
	}
	v, _ := a.CreatePost(ctx, app.CreatePostInput{})
	if strings.Join(v.Hashtags, ",") != "Donnervolk,indiegame" {
		t.Fatalf("brand hashtags not applied: %v", v.Hashtags)
	}
	s.DefaultPlatforms = []string{"nope"}
	if _, err := a.SaveSettings(ctx, s); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("unknown platform accepted: %v", err)
	}
}

// localFake is a platform that accepts http://localhost OAuth redirects (like TikTok).
type localFake struct{ *testutil.FakeAdapter }

func (localFake) AllowsLocalRedirect() bool { return true }

func TestLocalRedirectWithoutTunnel(t *testing.T) {
	a := testutil.NewApp(t, &testutil.FakeAdapter{Name: "remote"})
	a.Registry = platform.NewRegistry(&testutil.FakeAdapter{Name: "remote"}, localFake{&testutil.FakeAdapter{Name: "local"}})
	a.Cfg.PublicBaseURL = ""
	ctx := context.Background()

	list, err := a.Platforms(ctx)
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
	if _, err := a.StartConnect(ctx, "local"); err != nil {
		t.Fatalf("local connect: %v", err)
	}
	if _, err := a.StartConnect(ctx, "remote"); app.KindOf(err) != app.KindInvalid {
		t.Fatalf("remote connect without tunnel: %v", err)
	}
}
