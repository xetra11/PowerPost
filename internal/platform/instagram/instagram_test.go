package instagram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"powerposter/internal/platform"
)

func fakeGraph(t *testing.T, containerStatus func(poll int) string) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	var polls atomic.Int32
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

	mux.HandleFunc("POST /oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("code") != "the-code" || r.FormValue("grant_type") != "authorization_code" {
			http.Error(w, `{"error_message":"bad code"}`, 400)
			return
		}
		ok(w, map[string]any{"data": []any{map[string]any{"access_token": "short", "user_id": 123, "permissions": "x"}}})
	})
	mux.HandleFunc("GET /access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("grant_type") != "ig_exchange_token" || r.URL.Query().Get("access_token") != "short" {
			http.Error(w, "bad", 400)
			return
		}
		ok(w, map[string]any{"access_token": "long", "expires_in": 5184000})
	})
	mux.HandleFunc("GET /refresh_access_token", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"access_token": "refreshed", "expires_in": 5184000})
	})
	mux.HandleFunc("GET /v23.0/me", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"user_id": "17841", "username": "donnervolk"})
	})
	mux.HandleFunc("GET /v23.0/17841/content_publishing_limit", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "limit")
		ok(w, map[string]any{"data": []any{map[string]any{"quota_usage": 3, "config": map[string]any{"quota_total": 100}}}})
	})
	mux.HandleFunc("POST /v23.0/17841/media", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, fmt.Sprintf("create %s %s feed=%s thumb=%s token=%s", r.FormValue("media_type"), r.FormValue("upload_type"),
			r.FormValue("share_to_feed"), r.FormValue("thumb_offset"), r.FormValue("access_token")))
		if v := r.FormValue("cover_url"); v != "" {
			calls = append(calls, "cover_url "+v)
		}
		if v := r.FormValue("video_url"); v != "" {
			calls = append(calls, "video_url "+v)
		}
		if r.FormValue("caption") != "Hello #indiegame" {
			http.Error(w, `{"error":{"message":"caption mismatch","code":100}}`, 400)
			return
		}
		ok(w, map[string]any{"id": "C1"})
	})
	mux.HandleFunc("POST /ig-api-upload/v23.0/C1", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, fmt.Sprintf("upload %s size=%s offset=%s bytes=%d", r.Header.Get("Authorization"), r.Header.Get("file_size"), r.Header.Get("offset"), len(body)))
		ok(w, map[string]any{"success": true})
	})
	mux.HandleFunc("GET /v23.0/C1", func(w http.ResponseWriter, r *http.Request) {
		n := int(polls.Add(1))
		ok(w, map[string]any{"status_code": containerStatus(n), "status": "Error: video too short"})
	})
	mux.HandleFunc("POST /v23.0/17841/media_publish", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "publish "+r.FormValue("creation_id"))
		ok(w, map[string]any{"id": "M1"})
	})
	mux.HandleFunc("GET /v23.0/GONE", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Unsupported get request. Object with ID 'GONE' does not exist","type":"GraphMethodException","code":100,"error_subcode":33}}`, 400)
	})
	mux.HandleFunc("GET /v23.0/M1/insights", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"data": []any{map[string]any{"name": "views", "period": "lifetime", "values": []any{map[string]any{"value": 5321}}}}})
	})
	mux.HandleFunc("GET /v23.0/M2/insights", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"(#10) Application does not have permission for this action","code":10}}`, 403)
	})
	mux.HandleFunc("GET /v23.0/M2", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"like_count": 3, "comments_count": 0})
	})
	mux.HandleFunc("GET /v23.0/M1", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("fields"), "like_count") {
			ok(w, map[string]any{"like_count": 42, "comments_count": 7})
			return
		}
		ok(w, map[string]any{"permalink": "https://www.instagram.com/reel/abc/"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newTestAdapter(url string) *Adapter {
	a := New("app", "secret", "v23.0")
	a.AuthBase, a.TokenBase, a.GraphBase, a.UploadBase = url, url, url, url
	a.PollInterval = 10 * time.Millisecond
	a.PollTimeout = 2 * time.Second
	return a
}

func testTarget(t *testing.T) platform.ResolvedTarget {
	path := filepath.Join(t.TempDir(), "r.mp4")
	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	return platform.ResolvedTarget{Caption: "Hello #indiegame", Options: json.RawMessage(`{"share_to_feed":false}`), CoverMs: 1500, FilePath: path}
}

func testContext() platform.PublishContext {
	return platform.PublishContext{
		Token:    platform.TokenSet{AccessToken: "tok", AccountID: "17841"},
		Log:      func(string, ...any) {},
		Progress: func(string) {},
	}
}

func TestPublishReel(t *testing.T) {
	srv, calls := fakeGraph(t, func(n int) string {
		if n < 3 {
			return "IN_PROGRESS"
		}
		return "FINISHED"
	})
	a := newTestAdapter(srv.URL)
	res, err := a.Publish(context.Background(), testContext(), testTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteID != "M1" || res.Permalink != "https://www.instagram.com/reel/abc/" {
		t.Fatalf("unexpected result %+v", res)
	}
	want := []string{
		"limit",
		"create REELS resumable feed=false thumb=1500 token=tok",
		"upload OAuth tok size=4096 offset=0 bytes=4096",
		"publish C1",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestPublishReelRejected(t *testing.T) {
	srv, _ := fakeGraph(t, func(int) string { return "ERROR" })
	_, err := newTestAdapter(srv.URL).Publish(context.Background(), testContext(), testTarget(t))
	if err == nil || !strings.Contains(err.Error(), "video too short") {
		t.Fatalf("expected rejection, got %v", err)
	}
}

func TestExchangeAndRefresh(t *testing.T) {
	srv, _ := fakeGraph(t, func(int) string { return "FINISHED" })
	a := newTestAdapter(srv.URL)
	if u := a.AuthorizeURL("st", "https://x/cb", ""); !strings.Contains(u, "instagram_business_content_publish") || !strings.Contains(u, "state=st") {
		t.Fatalf("authorize url %s", u)
	}
	ts, err := a.Exchange(context.Background(), "the-code", "https://x/cb", "")
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken != "long" || ts.AccountID != "17841" || ts.AccountName != "donnervolk" || time.Until(ts.ExpiresAt) < 59*24*time.Hour {
		t.Fatalf("unexpected token %+v", ts)
	}
	fresh, err := a.Refresh(context.Background(), ts)
	if err != nil || fresh.AccessToken != "refreshed" || fresh.AccountID != "17841" {
		t.Fatalf("refresh: %+v %v", fresh, err)
	}
	if _, err := a.Exchange(context.Background(), "wrong", "https://x/cb", ""); err == nil || !strings.Contains(err.Error(), "bad code") {
		t.Fatalf("expected readable error, got %v", err)
	}
}

func TestImportToken(t *testing.T) {
	srv, _ := fakeGraph(t, func(int) string { return "FINISHED" })
	a := newTestAdapter(srv.URL)
	ts, err := a.ImportToken(context.Background(), "dashboard-token")
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken != "dashboard-token" || ts.AccountID != "17841" || ts.AccountName != "donnervolk" || time.Until(ts.ExpiresAt) < 59*24*time.Hour {
		t.Fatalf("unexpected token %+v", ts)
	}
}

func TestPublishReelViaPublicURL(t *testing.T) {
	srv, calls := fakeGraph(t, func(int) string { return "FINISHED" })
	pc := testContext()
	pc.PublicURL = func(string) (string, bool) { return "https://tunnel.example/public-media/abc", true }
	if _, err := newTestAdapter(srv.URL).Publish(context.Background(), pc, testTarget(t)); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(*calls, "\n")
	if !strings.Contains(got, "create REELS  feed=false") || !strings.Contains(got, "video_url https://tunnel.example/public-media/abc") || strings.Contains(got, "upload ") {
		t.Fatalf("calls:\n%s", got)
	}
}

func TestCoverURLAndIsLive(t *testing.T) {
	srv, calls := fakeGraph(t, func(int) string { return "FINISHED" })
	a := newTestAdapter(srv.URL)
	pc := testContext()
	pc.PublicURL = func(p string) (string, bool) { return "https://tunnel.example/public-media/" + filepath.Base(p), true }
	tg := testTarget(t)
	tg.CoverImagePath = filepath.Join(t.TempDir(), "cover.jpg")
	if _, err := a.Publish(context.Background(), pc, tg); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(*calls, "\n")
	if !strings.Contains(got, "cover_url https://tunnel.example/public-media/cover.jpg") || !strings.Contains(got, "thumb=") || strings.Contains(got, "thumb=1500") {
		t.Fatalf("custom cover must replace thumb_offset:\n%s", got)
	}

	if live, err := a.IsLive(context.Background(), platform.TokenSet{AccessToken: "tok"}, "M1"); err != nil || !live {
		t.Fatalf("M1: %v %v", live, err)
	}
	if live, err := a.IsLive(context.Background(), platform.TokenSet{AccessToken: "tok"}, "GONE"); err != nil || live {
		t.Fatalf("GONE: %v %v", live, err)
	}
}

func TestStatsAndEmbed(t *testing.T) {
	srv, _ := fakeGraph(t, func(int) string { return "FINISHED" })
	a := newTestAdapter(srv.URL)
	tok := platform.TokenSet{AccessToken: "tok"}
	s, err := a.Stats(context.Background(), tok, "M1")
	if err != nil || *s.Likes != 42 || *s.Comments != 7 || s.Views == nil || *s.Views != 5321 || s.Note != "" {
		t.Fatalf("M1 stats %+v %v", s, err)
	}
	s, err = a.Stats(context.Background(), tok, "M2") // no insights permission
	if err != nil || *s.Likes != 3 || s.Views != nil || !strings.Contains(s.Note, "instagram_business_manage_insights") {
		t.Fatalf("M2 stats %+v %v", s, err)
	}
	for in, want := range map[string]string{
		"https://www.instagram.com/reel/DAbc123/":    "https://www.instagram.com/reel/DAbc123/embed/",
		"https://www.instagram.com/p/XYZ/?igsh=1":    "https://www.instagram.com/p/XYZ/embed/",
		"https://example.com/reel/DAbc123/":          "",
		"https://www.instagram.com/donnervolk.game/": "",
	} {
		if got := a.EmbedURL(in, ""); got != want {
			t.Errorf("EmbedURL(%s) = %q, want %q", in, got, want)
		}
	}
}

// fakeImageGraph answers the image/carousel container calls and records them.
func fakeImageGraph(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	children := 0
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /v23.0/17841/content_publishing_limit", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"data": []any{}})
	})
	mux.HandleFunc("POST /v23.0/17841/media", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.FormValue("is_carousel_item") == "true":
			children++
			id := fmt.Sprintf("K%d", children)
			calls = append(calls, fmt.Sprintf("child %s caption=%q", r.FormValue("image_url"), r.FormValue("caption")))
			ok(w, map[string]any{"id": id})
		case r.FormValue("media_type") == "CAROUSEL":
			calls = append(calls, fmt.Sprintf("carousel %s caption=%q", r.FormValue("children"), r.FormValue("caption")))
			ok(w, map[string]any{"id": "C9"})
		default:
			calls = append(calls, fmt.Sprintf("image type=%q %s caption=%q", r.FormValue("media_type"), r.FormValue("image_url"), r.FormValue("caption")))
			ok(w, map[string]any{"id": "C9"})
		}
	})
	mux.HandleFunc("GET /v23.0/{id}", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("fields"), "permalink") {
			ok(w, map[string]any{"permalink": "https://www.instagram.com/p/xyz/"})
			return
		}
		calls = append(calls, "status "+r.PathValue("id"))
		ok(w, map[string]any{"status_code": "FINISHED"})
	})
	mux.HandleFunc("POST /v23.0/17841/media_publish", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "publish "+r.FormValue("creation_id"))
		ok(w, map[string]any{"id": "M9"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func imageTarget(n int) platform.ResolvedTarget {
	tg := platform.ResolvedTarget{Kind: platform.KindImage, Caption: "New biome", Options: json.RawMessage(`{}`)}
	for i := 1; i <= n; i++ {
		tg.Images = append(tg.Images, platform.File{Path: fmt.Sprintf("/data/r/%d.jpg", i)})
	}
	return tg
}

func publicContext() platform.PublishContext {
	pc := testContext()
	pc.PublicURL = func(path string) (string, bool) {
		return "https://pp.example.com/public-media/" + filepath.Base(path), true
	}
	return pc
}

func TestPublishImage(t *testing.T) {
	srv, calls := fakeImageGraph(t)
	res, err := newTestAdapter(srv.URL).Publish(context.Background(), publicContext(), imageTarget(1))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`image type="" https://pp.example.com/public-media/1.jpg caption="New biome"`,
		"status C9",
		"publish C9",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") || res.RemoteID != "M9" || res.Permalink != "https://www.instagram.com/p/xyz/" {
		t.Fatalf("calls:\n%s\nresult %+v", strings.Join(*calls, "\n"), res)
	}
}

func TestPublishCarousel(t *testing.T) {
	srv, calls := fakeImageGraph(t)
	if _, err := newTestAdapter(srv.URL).Publish(context.Background(), publicContext(), imageTarget(3)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`child https://pp.example.com/public-media/1.jpg caption=""`, "status K1",
		`child https://pp.example.com/public-media/2.jpg caption=""`, "status K2",
		`child https://pp.example.com/public-media/3.jpg caption=""`, "status K3",
		`carousel K1,K2,K3 caption="New biome"`, "status C9",
		"publish C9",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestPublishImageNeedsTunnel(t *testing.T) {
	srv, calls := fakeImageGraph(t)
	_, err := newTestAdapter(srv.URL).Publish(context.Background(), testContext(), imageTarget(2))
	if err == nil || !strings.Contains(err.Error(), "tunnel") || len(*calls) != 0 {
		t.Fatalf("expected tunnel error before any upload, got %v (calls %v)", err, *calls)
	}
}

func TestAccessBlockedHint(t *testing.T) {
	msg := graphError([]byte(`{"error":{"message":"API access blocked.","type":"OAuthException","code":200}}`))
	if !strings.Contains(msg, "API access blocked. (code 200)") || !strings.Contains(msg, "Meta app dashboard") {
		t.Fatalf("message %q", msg)
	}
}
