package youtube

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
	mu        sync.Mutex
	initBody  map[string]any
	initURL   *url.URL
	ranges    []string
	received  int
	polls     int
	privacy   string // privacyStatus YouTube reports after processing
	scope     string // scopes granted by the token endpoint
	tokenForm url.Values
}

func fakeAPI(t *testing.T, f *fake) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.tokenForm = r.PostForm
		f.mu.Unlock()
		if r.PostForm.Get("refresh_token") == "revoked" {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
			return
		}
		resp := map[string]any{"access_token": "tok", "expires_in": 3599, "scope": f.scope, "token_type": "Bearer"}
		if r.PostForm.Get("grant_type") == "authorization_code" {
			resp["refresh_token"] = "ref"
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /youtube/v3/channels", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("mine") != "true" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"items":[{"id":"UC123","snippet":{"title":"Donnervolk"}}]}`)
	})
	mux.HandleFunc("POST /upload/youtube/v3/videos", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.initURL = r.URL
		_ = json.NewDecoder(r.Body).Decode(&f.initBody)
		f.mu.Unlock()
		if r.Header.Get("X-Upload-Content-Length") == "" {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Location", srv.URL+"/session/1")
	})
	mux.HandleFunc("PUT /session/1", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		cr := r.Header.Get("Content-Range")
		f.ranges = append(f.ranges, cr)
		f.received += len(b)
		var start, end, total int
		fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total)
		if end+1 < total {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", end))
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"abcDEF12345","status":{"uploadStatus":"uploaded","privacyStatus":"public"}}`)
	})
	mux.HandleFunc("GET /youtube/v3/videos", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("id") != "abcDEF12345" {
			_, _ = io.WriteString(w, `{"items":[]}`)
			return
		}
		switch q.Get("part") {
		case "id":
			_, _ = io.WriteString(w, `{"items":[{"id":"abcDEF12345"}]}`)
		case "statistics":
			_, _ = io.WriteString(w, `{"items":[{"statistics":{"viewCount":"1200","commentCount":"4"}}]}`)
		default:
			f.mu.Lock()
			f.polls++
			done := f.polls > 1
			f.mu.Unlock()
			status, proc := "uploaded", "processing"
			if done {
				status, proc = "processed", "succeeded"
			}
			fmt.Fprintf(w, `{"items":[{"id":"abcDEF12345","status":{"uploadStatus":%q,"privacyStatus":%q},"processingDetails":{"processingStatus":%q,"processingProgress":{"partsTotal":"10","partsProcessed":"5"}}}]}`, status, f.privacy, proc)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newAdapter(srv *httptest.Server) *Adapter {
	a := New("cid", "secret")
	a.AuthBase = srv.URL
	a.TokenURL = srv.URL + "/token"
	a.APIBase = srv.URL + "/youtube/v3"
	a.UploadBase = srv.URL + "/upload/youtube/v3"
	a.ChunkSize = 10
	a.PollInterval = time.Millisecond
	return a
}

func opts(t *testing.T, o Options) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func defaults(t *testing.T) Options {
	t.Helper()
	var o Options
	if err := json.Unmarshal(New("", "").DefaultOptions(), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestAuthorizeURL(t *testing.T) {
	a := New("cid", "secret")
	u, err := url.Parse(a.AuthorizeURL("st", "http://localhost:4000/oauth/youtube/callback", "verifier"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "accounts.google.com" || q.Get("client_id") != "cid" || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Fatalf("url: %s", u)
	}
	if !strings.Contains(q.Get("scope"), "youtube.upload") || !strings.Contains(q.Get("scope"), "youtube.readonly") {
		t.Fatalf("scope: %s", q.Get("scope"))
	}
	if q.Get("code_challenge_method") != "S256" || strings.ContainsAny(q.Get("code_challenge"), "+/=") {
		t.Fatalf("challenge must be base64url without padding: %s", q.Get("code_challenge"))
	}
}

func TestExchangeAndRefresh(t *testing.T) {
	f := &fake{scope: scopeUpload + " " + scopeReadonly}
	a := newAdapter(fakeAPI(t, f))
	ts, err := a.Exchange(context.Background(), "code", "http://localhost/cb", "ver")
	if err != nil {
		t.Fatal(err)
	}
	if ts.AccountID != "UC123" || ts.AccountName != "Donnervolk" || ts.RefreshToken != "ref" || !ts.RefreshExpiresAt.IsZero() {
		t.Fatalf("token set: %+v", ts)
	}
	if f.tokenForm.Get("code_verifier") != "ver" || f.tokenForm.Get("client_secret") != "secret" {
		t.Fatalf("form: %v", f.tokenForm)
	}

	fresh, err := a.Refresh(context.Background(), ts)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.RefreshToken != "ref" || fresh.AccountName != "Donnervolk" || fresh.ExpiresAt.Before(time.Now().Add(50*time.Minute)) {
		t.Fatalf("refresh must keep the refresh token and account: %+v", fresh)
	}

	ts.RefreshToken = "revoked"
	if _, err := a.Refresh(context.Background(), ts); err == nil || !strings.Contains(err.Error(), "reconnect YouTube") {
		t.Fatalf("revoked refresh: %v", err)
	}
}

func TestExchangeMissingScope(t *testing.T) {
	f := &fake{scope: scopeReadonly} // user unchecked "upload videos"
	a := newAdapter(fakeAPI(t, f))
	if _, err := a.Exchange(context.Background(), "code", "http://localhost/cb", ""); err == nil || !strings.Contains(err.Error(), "both permissions") {
		t.Fatalf("err: %v", err)
	}
}

func TestPublish(t *testing.T) {
	f := &fake{privacy: "public"}
	a := newAdapter(fakeAPI(t, f))
	path := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 25)), 0o644); err != nil {
		t.Fatal(err)
	}
	o := defaults(t)
	o.NotifySubscribers = false
	var stages []string
	pc := platform.PublishContext{
		Token:    platform.TokenSet{AccessToken: "tok"},
		Log:      func(string, ...any) {},
		Progress: func(s string) { stages = append(stages, s) },
	}
	tgt := platform.ResolvedTarget{Kind: platform.KindVideo, Caption: "Chickens in the Iron Age\n\n#donnervolk #shorts", Options: opts(t, o), FilePath: path}
	res, err := a.Publish(context.Background(), pc, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if res.RemoteID != "abcDEF12345" || res.Permalink != "https://www.youtube.com/shorts/abcDEF12345" || res.Note != "" {
		t.Fatalf("result: %+v", res)
	}
	want := []string{"bytes 0-9/25", "bytes 10-19/25", "bytes 20-24/25"}
	if strings.Join(f.ranges, ",") != strings.Join(want, ",") || f.received != 25 {
		t.Fatalf("chunks: %v (%d bytes)", f.ranges, f.received)
	}
	q := f.initURL.Query()
	if q.Get("uploadType") != "resumable" || q.Get("part") != "snippet,status" || q.Get("notifySubscribers") != "false" {
		t.Fatalf("init query: %s", f.initURL.RawQuery)
	}
	snippet := f.initBody["snippet"].(map[string]any)
	status := f.initBody["status"].(map[string]any)
	if snippet["title"] != "Chickens in the Iron Age" || snippet["description"] != tgt.Caption || snippet["categoryId"] != "20" {
		t.Fatalf("snippet: %v", snippet)
	}
	if status["privacyStatus"] != "public" || status["selfDeclaredMadeForKids"] != false {
		t.Fatalf("status: %v", status)
	}
	if !strings.Contains(strings.Join(stages, "|"), "processing on YouTube (uploaded, processing 5/10)") {
		t.Fatalf("stages: %v", stages)
	}
}

func TestPublishLockedPrivate(t *testing.T) {
	f := &fake{privacy: "private"} // unaudited project: YouTube forces private
	a := newAdapter(fakeAPI(t, f))
	path := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	pc := platform.PublishContext{Token: platform.TokenSet{AccessToken: "tok"}, Log: func(string, ...any) {}, Progress: func(string) {}}
	res, err := a.Publish(context.Background(), pc, platform.ResolvedTarget{Caption: "Hi", Options: opts(t, defaults(t)), FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Note, "locked the video as private") {
		t.Fatalf("note: %q", res.Note)
	}
}

func TestValidate(t *testing.T) {
	a := New("", "")
	codes := func(caption string, o Options) map[string]bool {
		m := map[string]bool{}
		for _, is := range a.Validate(platform.ResolvedTarget{Caption: caption, Options: opts(t, o)}) {
			m[is.Code] = true
		}
		return m
	}
	o := defaults(t)
	if c := codes("First line\nmore", o); !c["TITLE_FROM_CAPTION"] || !c["AUDIT_NOTE"] || c["TITLE_REQUIRED"] {
		t.Fatalf("derived title: %v", c)
	}
	if c := codes("  \n", o); !c["TITLE_REQUIRED"] {
		t.Fatalf("empty: %v", c)
	}
	o.Title = strings.Repeat("a", 101)
	if c := codes("x", o); !c["TITLE_TOO_LONG"] {
		t.Fatalf("long: %v", c)
	}
	o.Title = "a <b>"
	o.PrivacyStatus = "friends"
	if c := codes("1 > 0", o); !c["TITLE_INVALID_CHARS"] || !c["DESCRIPTION_INVALID_CHARS"] || !c["PRIVACY_INVALID"] {
		t.Fatalf("invalid: %v", c)
	}
	o = defaults(t)
	o.PrivacyStatus = "private"
	if c := codes("ok", o); c["AUDIT_NOTE"] {
		t.Fatalf("private needs no audit note: %v", c)
	}
}

func TestTitleFromLongCaption(t *testing.T) {
	title, derived := Options{}.title(strings.Repeat("ä", 150))
	if !derived || len([]rune(title)) != maxTitle || !strings.HasSuffix(title, "…") {
		t.Fatalf("title %q (%d runes)", title, len([]rune(title)))
	}
}

func TestStatsLiveEmbed(t *testing.T) {
	a := newAdapter(fakeAPI(t, &fake{}))
	tok := platform.TokenSet{AccessToken: "tok"}
	s, err := a.Stats(context.Background(), tok, "abcDEF12345")
	if err != nil {
		t.Fatal(err)
	}
	if s.Views == nil || *s.Views != 1200 || s.Likes != nil || s.Comments == nil || *s.Comments != 4 {
		t.Fatalf("stats: %+v", s)
	}
	if live, err := a.IsLive(context.Background(), tok, "abcDEF12345"); err != nil || !live {
		t.Fatalf("live: %v %v", live, err)
	}
	if live, err := a.IsLive(context.Background(), tok, "deleted0000"); err != nil || live {
		t.Fatalf("deleted: %v %v", live, err)
	}
	if a.EmbedURL("", "abcDEF12345") != "https://www.youtube.com/embed/abcDEF12345" || a.EmbedURL("", "../x") != "" {
		t.Fatal("embed url")
	}
}

func TestGoogleError(t *testing.T) {
	body := []byte(`{"error":{"code":403,"message":"The request cannot be completed because you have exceeded your quota.","errors":[{"reason":"quotaExceeded","domain":"youtube.quota"}]}}`)
	if msg := googleError(body); !strings.HasPrefix(msg, "quotaExceeded: ") || !strings.Contains(msg, "midnight Pacific") {
		t.Fatalf("msg: %s", msg)
	}
	if googleError([]byte(`<html>`)) != "" {
		t.Fatal("non-JSON body")
	}
}
