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
	mux.HandleFunc("GET /v23.0/M1", func(w http.ResponseWriter, r *http.Request) {
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
