package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"powerposter/internal/api"
	"powerposter/internal/store"
	"powerposter/internal/testutil"
)

func newServer(t *testing.T) (*api.Server, http.Handler) {
	a := testutil.NewApp(t, &testutil.FakeAdapter{Name: "fake"})
	s := &api.Server{
		App: a,
		Web: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app.js": {Data: []byte("js")}},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return s, s.PrivateHandler()
}

func do(h http.Handler, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:4000"+path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSecurityMiddleware(t *testing.T) {
	_, h := newServer(t)
	req := httptest.NewRequest("GET", "http://evil.example.com/api/posts", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign host: %d", rec.Code)
	}
	rec = do(h, "POST", "/api/posts", strings.NewReader(`{"projectId":1}`), map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site write: %d", rec.Code)
	}
	rec = do(h, "POST", "/api/posts", strings.NewReader(`{"projectId":1}`), map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("same-origin create: %d %s", rec.Code, rec.Body)
	}
}

func TestUploadAndErrors(t *testing.T) {
	s, h := newServer(t)
	rec := do(h, "POST", "/api/posts", strings.NewReader(`{"projectId":1,"caption":"hi"}`), nil)
	var post struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &post)

	host := testutil.MakeVideo(t, s.App, "up.mp4", 320, 240, 30, 1)
	local, _ := s.App.ResolveInboxPath(host)
	data, _ := os.ReadFile(local)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "up.mp4")
	fw.Write(data)
	mw.Close()
	rec = do(h, "POST", "/api/posts/1/media", &buf, map[string]string{"Content-Type": mw.FormDataContentType()})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"originalName":"up.mp4"`) {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(s.App.Cfg.DataDir, "media", "originals")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/posts/999", "", 404},
		{"GET", "/api/posts/abc", "", 400},
		{"PATCH", "/api/posts/1", `{"unknownField":1}`, 400},
		{"PATCH", "/api/posts/1", `{"aspectStrategy":"stretch"}`, 400},
		{"POST", "/api/posts/1/publish", `{}`, 409},
		{"GET", "/api/nope", "", 404},
	}
	for _, c := range cases {
		rec := do(h, c.method, c.path, strings.NewReader(c.body), map[string]string{"Content-Type": "application/json"})
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s %s: got %d %s, want %d", c.method, c.path, rec.Code, rec.Body, c.want)
		}
	}
}

func TestWebAndMedia(t *testing.T) {
	_, h := newServer(t)
	if rec := do(h, "GET", "/posts/12/review", nil, nil); rec.Code != 200 || rec.Body.String() != "<html>app</html>" {
		t.Fatalf("SPA fallback: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/assets/app.js", nil, nil); rec.Body.String() != "js" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %s %v", rec.Body, rec.Header())
	}
	if rec := do(h, "GET", "/media/originals/..%2f..%2fsecret.key", nil, nil); rec.Code != 404 {
		t.Fatalf("media traversal: %d", rec.Code)
	}
	if rec := do(h, "GET", "/media/other/x", nil, nil); rec.Code != 404 {
		t.Fatalf("unknown media kind: %d", rec.Code)
	}
}

func TestOAuthCallback(t *testing.T) {
	s, _ := newServer(t)
	pub := s.PublicHandler()

	rec := do(pub, "GET", "/oauth/fake/callback?code=c&state=unknown", nil, nil)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "connect_error=") {
		t.Fatalf("bad state: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if err := s.App.Store.SaveOAuthState(t.Context(), "good", store.OAuthState{ProjectID: testutil.Project, Platform: "fake"}); err != nil {
		t.Fatal(err)
	}
	rec = do(pub, "GET", "/oauth/fake/callback?code=c&state=good", nil, nil)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "http://127.0.0.1:4000/settings?project=1&connected=fake" {
		t.Fatalf("good state: %d %s", rec.Code, loc)
	}
	// The public listener exposes nothing else.
	for _, p := range []string{"/api/posts", "/mcp", "/", "/media/originals/x"} {
		if rec := do(pub, "GET", p, nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("public %s: %d", p, rec.Code)
		}
	}
}

func TestPublicMediaLinks(t *testing.T) {
	s, private := newServer(t)
	pub := s.PublicHandler()
	file := filepath.Join(s.App.Cfg.DataDir, "media", "renditions", "9-abc.mp4")
	if err := os.WriteFile(file, []byte("video-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	link, ok := s.App.PublicMediaURL(file, time.Hour)
	if !ok || !strings.HasPrefix(link, "https://pp.example.com/public-media/") {
		t.Fatalf("link %q %v", link, ok)
	}
	path := strings.TrimPrefix(link, "https://pp.example.com")
	if rec := do(pub, "GET", path, nil, nil); rec.Code != 200 || rec.Body.String() != "video-bytes" {
		t.Fatalf("valid link: %d %q", rec.Code, rec.Body)
	}
	expired, _ := s.App.PublicMediaURL(file, -time.Minute)
	for _, bad := range []string{
		path + "x", // tampered signature
		strings.Replace(path, "9-abc", "9-abd", 1),            // other file
		strings.TrimPrefix(expired, "https://pp.example.com"), // expired
	} {
		if rec := do(pub, "GET", bad, nil, nil); rec.Code != 404 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// The private listener doesn't serve public-media links as files.
	if rec := do(private, "GET", path, nil, nil); rec.Body.String() == "video-bytes" {
		t.Fatal("private listener served the file")
	}

	// Converted images go out with their own content type.
	img := filepath.Join(s.App.Cfg.DataDir, "media", "renditions", "9-def.jpg")
	if err := os.WriteFile(img, []byte("jpeg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	link, _ = s.App.PublicMediaURL(img, time.Hour)
	if rec := do(pub, "GET", strings.TrimPrefix(link, "https://pp.example.com"), nil, nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("image link: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestTikTokVerificationFile(t *testing.T) {
	s, private := newServer(t)
	pub := s.PublicHandler()
	for name, body := range map[string]string{"tiktokAbC123.txt": "tiktok-developers-site-verification=x", "secret.txt": "no", "clip.mp4": "no"} {
		if err := os.WriteFile(filepath.Join(s.App.Cfg.InboxDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if rec := do(pub, "GET", "/tiktokAbC123.txt", nil, nil); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "tiktok-developers") {
		t.Fatalf("signature file: %d %q", rec.Code, rec.Body)
	}
	// Nothing else from the inbox is public.
	for _, p := range []string{"/secret.txt", "/clip.mp4", "/tiktokMissing.txt", "/tiktok..txt"} {
		if rec := do(pub, "GET", p, nil, nil); rec.Code != 404 {
			t.Errorf("public %s: %d", p, rec.Code)
		}
	}
	if rec := do(private, "GET", "/tiktokAbC123.txt", nil, nil); strings.HasPrefix(rec.Body.String(), "tiktok-developers") {
		t.Fatal("private listener served the signature file")
	}
}

func TestProjectsAPI(t *testing.T) {
	_, h := newServer(t)
	json1 := map[string]string{"Content-Type": "application/json"}

	rec := do(h, "POST", "/api/projects", strings.NewReader(`{"name":"Sky Forge"}`), json1)
	var proj struct {
		ID       int64  `json:"id"`
		Slug     string `json:"slug"`
		Settings struct {
			Brand struct {
				GameName string `json:"gameName"`
			} `json:"brand"`
		} `json:"settings"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &proj) != nil || proj.Slug != "sky-forge" || proj.Settings.Brand.GameName != "Sky Forge" {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body)
	}
	pid := strconv.FormatInt(proj.ID, 10)

	if rec := do(h, "POST", "/api/posts", strings.NewReader(`{"projectId":`+pid+`,"title":"Sky post"}`), json1); rec.Code != http.StatusCreated {
		t.Fatalf("create post: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "POST", "/api/posts", strings.NewReader(`{"title":"No project"}`), json1); rec.Code != http.StatusBadRequest {
		t.Fatalf("post without project: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "GET", "/api/posts?project="+pid, nil, nil)
	if !strings.Contains(rec.Body.String(), "Sky post") || strings.Count(rec.Body.String(), `"title"`) != 1 {
		t.Fatalf("posts of project: %s", rec.Body)
	}
	if rec := do(h, "GET", "/api/posts?project=1", nil, nil); strings.Contains(rec.Body.String(), "Sky post") {
		t.Fatalf("post leaked into project 1: %s", rec.Body)
	}

	// Connections are per project.
	rec = do(h, "GET", "/api/projects/"+pid+"/platforms", nil, nil)
	if !strings.Contains(rec.Body.String(), `"connected":false`) {
		t.Fatalf("new project should have no connections: %s", rec.Body)
	}
	if rec := do(h, "GET", "/api/projects/1/platforms", nil, nil); !strings.Contains(rec.Body.String(), `"connected":true`) {
		t.Fatalf("project 1 connection: %s", rec.Body)
	}

	if rec := do(h, "DELETE", "/api/projects/"+pid, nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete project with posts: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/projects/999/platforms", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", rec.Code)
	}
}
