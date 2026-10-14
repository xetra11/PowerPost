package tiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"powerposter/internal/platform"
)

type fake struct {
	mu       sync.Mutex
	initBody map[string]any
	initPath string
	ranges   []string
	received int
}

func fakeAPI(t *testing.T, f *fake) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	envelope := func(w http.ResponseWriter, data any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": map[string]any{"code": "ok", "message": "", "log_id": "l"}})
	}
	mux.HandleFunc("POST /v2/post/publish/creator_info/query/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "access_token_invalid", "message": "bad token"}})
			return
		}
		envelope(w, map[string]any{"creator_username": "donnervolk", "creator_nickname": "Donnervolk",
			"privacy_level_options": []string{"PUBLIC_TO_EVERYONE", "SELF_ONLY"}, "max_video_post_duration_sec": 600, "duet_disabled": true})
	})
	initHandler := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.initPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&f.initBody)
		f.mu.Unlock()
		envelope(w, map[string]any{"publish_id": "p1", "upload_url": srv.URL + "/upload"})
	}
	mux.HandleFunc("POST /v2/post/publish/video/init/", initHandler)
	mux.HandleFunc("POST /v2/post/publish/inbox/video/init/", initHandler)
	mux.HandleFunc("POST /v2/post/publish/content/init/", initHandler)
	mux.HandleFunc("PUT /upload", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.ranges = append(f.ranges, r.Header.Get("Content-Range"))
		f.received += len(b)
		f.mu.Unlock()
		w.WriteHeader(201)
	})
	mux.HandleFunc("POST /v2/post/publish/status/fetch/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		inbox := strings.Contains(f.initPath, "inbox") || f.initBody["post_mode"] == "MEDIA_UPLOAD"
		f.mu.Unlock()
		if inbox {
			envelope(w, map[string]any{"status": "SEND_TO_USER_INBOX"})
			return
		}
		envelope(w, map[string]any{"status": "PUBLISH_COMPLETE", "publicaly_available_post_id": []any{7123456789}})
	})
	mux.HandleFunc("POST /v2/oauth/token/", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("code") == "c" && r.FormValue("code_verifier") != "verifier-123" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_request", "error_description": "code_verifier missing"})
			return
		}
		if r.FormValue("code") == "bad" {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "Authorization code is expired."})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 86400, "open_id": "o1",
			"refresh_token": "rt", "refresh_expires_in": 31536000, "scope": "user.info.basic,video.publish"})
	})
	mux.HandleFunc("GET /v2/user/info/", func(w http.ResponseWriter, r *http.Request) {
		envelope(w, map[string]any{"user": map[string]any{"open_id": "o1", "display_name": "Donnervolk"}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestAdapter(url string) *Adapter {
	a := New("key", "secret")
	a.AuthBase, a.APIBase = url, url
	a.PollInterval = 10 * time.Millisecond
	a.PollTimeout = 2 * time.Second
	return a
}

func target(t *testing.T, opts string, size int) platform.ResolvedTarget {
	p := filepath.Join(t.TempDir(), "r.mp4")
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	return platform.ResolvedTarget{Caption: "Raid! #indiegame", Options: json.RawMessage(opts), CoverMs: 1000, FilePath: p}
}

func pc(token string) platform.PublishContext {
	return platform.PublishContext{Token: platform.TokenSet{AccessToken: token}, Log: func(string, ...any) {}, Progress: func(string) {}}
}

func TestDirectPost(t *testing.T) {
	f := &fake{}
	a := newTestAdapter(fakeAPI(t, f).URL)
	opts := `{"mode":"direct","privacy_level":"PUBLIC_TO_EVERYONE","commercial_content":true,"brand_organic":true}`
	res, err := a.Publish(context.Background(), pc("tok"), target(t, opts, 2048))
	if err != nil {
		t.Fatal(err)
	}
	if res.Permalink != "https://www.tiktok.com/@donnervolk/video/7123456789" {
		t.Fatalf("permalink %q", res.Permalink)
	}
	pi := f.initBody["post_info"].(map[string]any)
	si := f.initBody["source_info"].(map[string]any)
	if pi["title"] != "Raid! #indiegame" || pi["privacy_level"] != "PUBLIC_TO_EVERYONE" || pi["brand_organic_toggle"] != true ||
		pi["brand_content_toggle"] != false || pi["disable_duet"] != true /* forced by creator settings */ {
		t.Fatalf("post_info %v", pi)
	}
	if si["source"] != "FILE_UPLOAD" || si["video_size"] != float64(2048) || si["total_chunk_count"] != float64(1) {
		t.Fatalf("source_info %v", si)
	}
	if fmt.Sprint(f.ranges) != "[bytes 0-2047/2048]" || f.received != 2048 {
		t.Fatalf("upload ranges %v received %d", f.ranges, f.received)
	}
}

func TestInboxUpload(t *testing.T) {
	f := &fake{}
	a := newTestAdapter(fakeAPI(t, f).URL)
	res, err := a.Publish(context.Background(), pc("tok"), target(t, `{"mode":"inbox"}`, 100))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.initPath, "/inbox/") || f.initBody["post_info"] != nil || !strings.Contains(res.Note, "inbox") {
		t.Fatalf("path %s body %v note %q", f.initPath, f.initBody, res.Note)
	}
}

func TestPrivacyNotAllowedForCreator(t *testing.T) {
	a := newTestAdapter(fakeAPI(t, &fake{}).URL)
	_, err := a.Publish(context.Background(), pc("tok"), target(t, `{"mode":"direct","privacy_level":"FOLLOWER_OF_CREATOR"}`, 10))
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected privacy error, got %v", err)
	}
}

func TestBadTokenError(t *testing.T) {
	a := newTestAdapter(fakeAPI(t, &fake{}).URL)
	_, err := a.Publish(context.Background(), pc("nope"), target(t, `{"mode":"inbox"}`, 10))
	if err == nil || !strings.Contains(err.Error(), "access_token_invalid") {
		t.Fatalf("expected readable error, got %v", err)
	}
}

func TestChunking(t *testing.T) {
	const mb = 1 << 20
	for _, c := range []struct{ size, chunk, count int64 }{
		{1 * mb, 1 * mb, 1},
		{64 * mb, 64 * mb, 1},
		{205 * mb, 10 * mb, 20}, // last chunk carries 15 MB
	} {
		chunk, count := chunking(c.size)
		if chunk != c.chunk || count != c.count {
			t.Errorf("chunking(%d) = %d, %d", c.size, chunk, count)
		}
		if last := c.size - (count-1)*chunk; last > 128*mb {
			t.Errorf("last chunk too big: %d", last)
		}
	}
}

func TestValidate(t *testing.T) {
	a := New("k", "s")
	codes := func(opts string) []string {
		var out []string
		for _, i := range a.Validate(platform.ResolvedTarget{Options: json.RawMessage(opts)}) {
			if i.Severity == platform.SeverityError {
				out = append(out, i.Code)
			}
		}
		return out
	}
	if c := codes(`{"mode":"inbox"}`); len(c) != 0 {
		t.Fatalf("inbox: %v", c)
	}
	if c := codes(`{"mode":"direct","privacy_level":""}`); fmt.Sprint(c) != "[PRIVACY_REQUIRED]" {
		t.Fatalf("missing privacy: %v", c)
	}
	if c := codes(`{"mode":"direct","privacy_level":"SELF_ONLY","commercial_content":true,"brand_content":true}`); fmt.Sprint(c) != "[BRANDED_PRIVATE]" {
		t.Fatalf("branded private: %v", c)
	}
	if c := codes(`{"mode":"direct","privacy_level":"PUBLIC_TO_EVERYONE","commercial_content":true}`); fmt.Sprint(c) != "[DISCLOSURE_INCOMPLETE]" {
		t.Fatalf("disclosure: %v", c)
	}
	if n := a.ReviewNotices(json.RawMessage(`{"mode":"direct","commercial_content":true,"brand_organic":true}`)); len(n) != 2 {
		t.Fatalf("notices %v", n)
	}
}

func TestExchange(t *testing.T) {
	a := newTestAdapter(fakeAPI(t, &fake{}).URL)
	ts, err := a.Exchange(context.Background(), "c", "https://x/cb", "verifier-123")
	if err != nil || ts.AccessToken != "tok" || ts.RefreshToken != "rt" || ts.AccountName != "Donnervolk" || ts.AccountID != "o1" {
		t.Fatalf("%+v %v", ts, err)
	}
	if _, err := a.Exchange(context.Background(), "bad", "https://x/cb", ""); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected readable error, got %v", err)
	}
}

func TestAuthorizeURLUsesHexPKCE(t *testing.T) {
	a := New("key", "secret")
	u, err := url.Parse(a.AuthorizeURL("st", "http://localhost:4000/oauth/tiktok/callback", "abc"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	// sha256("abc") in hex — TikTok's PKCE variant.
	if q.Get("code_challenge") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("challenge %q method %q", q.Get("code_challenge"), q.Get("code_challenge_method"))
	}
	if q.Get("redirect_uri") != "http://localhost:4000/oauth/tiktok/callback" || q.Get("state") != "st" {
		t.Fatalf("query %v", q)
	}
	if !a.AllowsLocalRedirect() {
		t.Fatal("TikTok should allow localhost redirects")
	}
}

func TestEmbedURL(t *testing.T) {
	a := New("k", "s")
	if got := a.EmbedURL("https://www.tiktok.com/@dv/video/7123456789", "7123456789"); got != "https://www.tiktok.com/embed/v2/7123456789" {
		t.Fatalf("got %q", got)
	}
	if a.EmbedURL("", "p1") != "" || a.EmbedURL("https://www.tiktok.com/@dv/video/x", "v_pub_x") != "" {
		t.Fatal("inbox/private posts must not be embeddable")
	}
}

func photoTarget(opts string) platform.ResolvedTarget {
	return platform.ResolvedTarget{Kind: platform.KindImage, Caption: "New biome #indiegame", Options: json.RawMessage(opts),
		Images: []platform.File{{Path: "/data/r/1.jpg"}, {Path: "/data/r/2.jpg"}}}
}

func publicPC() platform.PublishContext {
	p := pc("tok")
	p.PublicURL = func(path string) (string, bool) {
		return "https://pp.example.com/public-media/" + filepath.Base(path), true
	}
	return p
}

func TestPhotoPostDirect(t *testing.T) {
	f := &fake{}
	a := newTestAdapter(fakeAPI(t, f).URL)
	opts := `{"mode":"direct","privacy_level":"SELF_ONLY","auto_add_music":true,"commercial_content":true,"brand_organic":true}`
	res, err := a.Publish(context.Background(), publicPC(), photoTarget(opts))
	if err != nil {
		t.Fatal(err)
	}
	if f.initPath != "/v2/post/publish/content/init/" || f.initBody["media_type"] != "PHOTO" || f.initBody["post_mode"] != "DIRECT_POST" {
		t.Fatalf("init %s %v", f.initPath, f.initBody)
	}
	pi := f.initBody["post_info"].(map[string]any)
	si := f.initBody["source_info"].(map[string]any)
	if pi["description"] != "New biome #indiegame" || pi["privacy_level"] != "SELF_ONLY" || pi["auto_add_music"] != true || pi["brand_organic_toggle"] != true {
		t.Fatalf("post_info %v", pi)
	}
	if si["source"] != "PULL_FROM_URL" || fmt.Sprint(si["photo_images"]) != "[https://pp.example.com/public-media/1.jpg https://pp.example.com/public-media/2.jpg]" {
		t.Fatalf("source_info %v", si)
	}
	if res.Permalink != "https://www.tiktok.com/@donnervolk/photo/7123456789" {
		t.Fatalf("permalink %q", res.Permalink)
	}
}

func TestPhotoPostInboxAndTunnel(t *testing.T) {
	f := &fake{}
	a := newTestAdapter(fakeAPI(t, f).URL)
	res, err := a.Publish(context.Background(), publicPC(), photoTarget(`{"mode":"inbox"}`))
	if err != nil {
		t.Fatal(err)
	}
	pi := f.initBody["post_info"].(map[string]any)
	if f.initBody["post_mode"] != "MEDIA_UPLOAD" || pi["description"] != "New biome #indiegame" || pi["privacy_level"] != nil || !strings.Contains(res.Note, "inbox") {
		t.Fatalf("init %v note %q", f.initBody, res.Note)
	}
	// Without the tunnel TikTok can't fetch the photos.
	if _, err := a.Publish(context.Background(), pc("tok"), photoTarget(`{"mode":"inbox"}`)); err == nil || !strings.Contains(err.Error(), "tunnel") {
		t.Fatalf("expected tunnel error, got %v", err)
	}
}
