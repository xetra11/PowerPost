package discord

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
	"sync"
	"testing"

	"powerposter/internal/media"
	"powerposter/internal/platform"
)

const hookURL = "https://discord.com/api/webhooks/111/secret-token"

type fake struct {
	mu       sync.Mutex
	payload  map[string]any
	query    string
	file     []byte
	filename string
	files    []string // "field name:content type:bytes" of every file part
	limited  int      // answer 429 this many times first
	deleted  bool
}

func fakeAPI(t *testing.T, f *fake) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /webhooks/111/secret-token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "111", "name": "Donnervolk News", "guild_id": "900", "channel_id": "500"})
	})
	mux.HandleFunc("GET /webhooks/111/wrong", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Invalid Webhook Token", "code": 50027})
	})
	mux.HandleFunc("POST /webhooks/111/secret-token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.limited > 0 {
			f.limited--
			w.WriteHeader(429)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "You are being rate limited.", "retry_after": 0.01, "code": 0})
			return
		}
		f.query = r.URL.RawQuery
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		_ = json.Unmarshal([]byte(r.FormValue("payload_json")), &f.payload)
		f.files = nil
		for i := 0; ; i++ {
			key := fmt.Sprintf("files[%d]", i)
			fhs := r.MultipartForm.File[key]
			if len(fhs) == 0 {
				break
			}
			fh, _ := fhs[0].Open()
			b, _ := io.ReadAll(fh)
			fh.Close()
			f.files = append(f.files, fmt.Sprintf("%s:%s:%s", fhs[0].Filename, fhs[0].Header.Get("Content-Type"), b))
		}
		file, hdr, err := r.FormFile("files[0]")
		if err != nil {
			t.Errorf("file: %v", err)
		} else {
			f.file, _ = io.ReadAll(file)
			f.filename = hdr.Filename
		}
		channel := "500"
		if f.payload["thread_name"] != nil {
			channel = "777" // forum post: a new thread
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "42", "channel_id": channel})
	})
	mux.HandleFunc("GET /webhooks/111/secret-token/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.deleted {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Unknown Message", "code": 10008})
			return
		}
		f.query = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "channel_id": "500",
			"reactions": []any{map[string]any{"count": 3}, map[string]any{"count": 2}}, "thread": map[string]any{"message_count": 4}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newAdapter(srv *httptest.Server) *Adapter {
	a := New(0)
	a.APIBase = srv.URL
	return a
}

func target(t *testing.T, opts string) platform.ResolvedTarget {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.mp4")
	if err := os.WriteFile(path, []byte("fake-mp4-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return platform.ResolvedTarget{PostID: 7, Kind: platform.KindVideo, Caption: "Raid night! @everyone #donnervolk", Options: json.RawMessage(opts), FilePath: path}
}

func pc(token string) platform.PublishContext {
	return platform.PublishContext{Token: platform.TokenSet{AccessToken: token}, Log: func(string, ...any) {}, Progress: func(string) {}}
}

func TestParseWebhook(t *testing.T) {
	for _, u := range []string{hookURL, "https://discordapp.com/api/webhooks/111/secret-token", "https://ptb.discord.com/api/v10/webhooks/111/secret-token/", "https://discord.com/api/webhooks/111/secret-token?wait=true"} {
		h, err := parseWebhook(u)
		if err != nil || h.id != "111" || h.token != "secret-token" {
			t.Errorf("%s: %+v %v", u, h, err)
		}
	}
	for _, u := range []string{"", "https://evil.com/api/webhooks/1/x", "http://discord.com/api/webhooks/1/x", "https://discord.com/channels/1/2"} {
		if _, err := parseWebhook(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestImportToken(t *testing.T) {
	a := newAdapter(fakeAPI(t, &fake{}))
	ts, err := a.ImportToken(context.Background(), hookURL)
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccessToken != hookURL || ts.AccountName != "Donnervolk News" || ts.AccountID != "500" || !ts.ExpiresAt.IsZero() {
		t.Errorf("token set: %+v", ts)
	}
	if _, err := a.ImportToken(context.Background(), "https://discord.com/api/webhooks/111/wrong"); err == nil || !strings.Contains(err.Error(), "Invalid Webhook Token") {
		t.Errorf("bad webhook: %v", err)
	}
}

func TestPublish(t *testing.T) {
	f := &fake{limited: 1}
	a := newAdapter(fakeAPI(t, f))
	res, err := a.Publish(context.Background(), pc(hookURL), target(t, `{"mention":"none"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteID != "42" || res.Permalink != "https://discord.com/channels/900/500/42" {
		t.Errorf("result: %+v", res)
	}
	if f.query != "wait=true" || string(f.file) != "fake-mp4-bytes" || f.filename != "post-7.mp4" {
		t.Errorf("request: query=%q file=%q name=%q", f.query, f.file, f.filename)
	}
	if f.payload["content"] != "Raid night! @everyone #donnervolk" {
		t.Errorf("content: %v", f.payload["content"])
	}
	// A caption mentioning @everyone must not ping without the option.
	if parse := f.payload["allowed_mentions"].(map[string]any)["parse"]; strings.Contains(strings.Join(toStrings(parse), ","), "everyone") {
		t.Errorf("allowed_mentions: %v", parse)
	}
}

func TestPublishPingAndForum(t *testing.T) {
	f := &fake{}
	a := newAdapter(fakeAPI(t, f))
	res, err := a.Publish(context.Background(), pc(hookURL), target(t, `{"mention":"here","thread_name":"New raid"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.payload["content"].(string), "@here\n") || f.payload["thread_name"] != "New raid" {
		t.Errorf("payload: %v", f.payload)
	}
	if parse := toStrings(f.payload["allowed_mentions"].(map[string]any)["parse"]); !strings.Contains(strings.Join(parse, ","), "everyone") {
		t.Errorf("allowed_mentions: %v", parse)
	}
	if res.RemoteID != "777/42" || res.Permalink != "https://discord.com/channels/900/777/42" {
		t.Errorf("result: %+v", res)
	}

	// Messages in a thread are read with thread_id.
	if _, err := a.Stats(context.Background(), platform.TokenSet{AccessToken: hookURL}, res.RemoteID); err != nil {
		t.Fatal(err)
	}
	if f.query != "thread_id=777" {
		t.Errorf("query: %q", f.query)
	}
}

func TestStatsAndLive(t *testing.T) {
	f := &fake{}
	a := newAdapter(fakeAPI(t, f))
	tok := platform.TokenSet{AccessToken: hookURL}
	s, err := a.Stats(context.Background(), tok, "42")
	if err != nil {
		t.Fatal(err)
	}
	if s.Likes == nil || *s.Likes != 5 || s.Comments == nil || *s.Comments != 4 || s.Views != nil {
		t.Errorf("stats: %+v", s)
	}
	if live, err := a.IsLive(context.Background(), tok, "42"); !live || err != nil {
		t.Errorf("live: %v %v", live, err)
	}
	f.deleted = true
	if live, err := a.IsLive(context.Background(), tok, "42"); live || err != nil {
		t.Errorf("deleted: %v %v", live, err)
	}
}

func TestErrorsHideToken(t *testing.T) {
	a := New(0)
	a.APIBase = "http://127.0.0.1:1" // nothing listens here
	_, err := a.Publish(context.Background(), pc(hookURL), target(t, `{}`))
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error leaks the webhook token: %v", err)
	}
}

func TestValidate(t *testing.T) {
	a := New(0)
	tg := target(t, `{"mention":"loud","thread_name":"`+strings.Repeat("x", 101)+`"}`)
	tg.File.Duration, tg.File.Size = 60, 10<<20 // ~1400 kbps
	codes := map[string]platform.Severity{}
	for _, i := range a.Validate(tg) {
		codes[i.Code] = i.Severity
	}
	if codes["OPTIONS_INVALID"] != platform.SeverityError || codes["THREAD_NAME_TOO_LONG"] != platform.SeverityError || codes["LOW_QUALITY"] != platform.SeverityWarning {
		t.Errorf("issues: %v", codes)
	}

	tg = target(t, `{"mention":"everyone"}`)
	tg.Caption = strings.Repeat("a", 1995)
	if got := a.Validate(tg); len(got) != 1 || got[0].Code != "CAPTION_TOO_LONG" {
		t.Errorf("ping over limit: %+v", got)
	}
	if n := a.ReviewNotices(json.RawMessage(`{"mention":"everyone"}`)); len(n) != 1 {
		t.Errorf("notices: %v", n)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func TestPublishImages(t *testing.T) {
	f := &fake{}
	a := newAdapter(fakeAPI(t, f))
	dir := t.TempDir()
	var images []platform.File
	for i, name := range []string{"a.png", "b.jpg"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(fmt.Sprintf("image-%d", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		images = append(images, platform.File{Path: p})
	}
	tg := platform.ResolvedTarget{PostID: 7, Kind: platform.KindImage, Caption: "New biome", Options: json.RawMessage(`{"mention":"none"}`), Images: images}
	res, err := a.Publish(context.Background(), pc(hookURL), tg)
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteID != "42" {
		t.Errorf("result: %+v", res)
	}
	want := "post-7-1.png:image/png:image-1 post-7-2.jpg:image/jpeg:image-2"
	if got := strings.Join(f.files, " "); got != want {
		t.Errorf("files:\n%s\nwant:\n%s", got, want)
	}
	if att := f.payload["attachments"].([]any); len(att) != 2 || att[1].(map[string]any)["filename"] != "post-7-2.jpg" {
		t.Errorf("attachments: %v", att)
	}
}

func TestValidateImagesTotalSize(t *testing.T) {
	a := New(10)
	tg := platform.ResolvedTarget{Kind: platform.KindImage, Options: json.RawMessage(`{}`), Images: []platform.File{
		{Info: media.Info{Size: 6 << 20}}, {Info: media.Info{Size: 6 << 20}},
	}}
	if v := a.Validate(tg); len(v) != 1 || v[0].Code != "FILES_TOO_LARGE" {
		t.Fatalf("issues %v", v)
	}
}
