// Package api serves the REST API, media files, the web UI and OAuth callbacks.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"powerposter/internal/app"
)

type Server struct {
	App *app.App
	Web fs.FS        // built UI; nil when not available
	MCP http.Handler // mounted at /mcp
	Log *slog.Logger
}

// PrivateHandler serves everything for the local user: UI, API, media, MCP.
func (s *Server) PrivateHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)

	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/posts", s.listPosts)
	mux.HandleFunc("POST /api/posts", s.createPost)
	mux.HandleFunc("GET /api/posts/{id}", s.getPost)
	mux.HandleFunc("PATCH /api/posts/{id}", s.updatePost)
	mux.HandleFunc("DELETE /api/posts/{id}", s.deletePost)
	mux.HandleFunc("PATCH /api/posts/{id}/targets/{platform}", s.updateTarget)
	mux.HandleFunc("POST /api/posts/{id}/media", s.uploadMedia)
	mux.HandleFunc("POST /api/posts/{id}/approve", s.approve)
	mux.HandleFunc("POST /api/posts/{id}/unapprove", s.unapprove)
	mux.HandleFunc("POST /api/posts/{id}/publish", s.publish)
	mux.HandleFunc("GET /api/posts/{id}/attempts", s.attempts)
	mux.HandleFunc("POST /api/posts/{id}/check-live", s.checkLive)
	mux.HandleFunc("POST /api/posts/{id}/duplicate", s.duplicate)
	mux.HandleFunc("POST /api/posts/{id}/cover", s.uploadCover)
	mux.HandleFunc("DELETE /api/posts/{id}/cover", s.clearCover)
	mux.HandleFunc("GET /api/platforms", s.platforms)
	mux.HandleFunc("GET /api/platforms/{platform}/account-info", s.accountInfo)
	mux.HandleFunc("POST /api/connections/{platform}/start", s.startConnect)
	mux.HandleFunc("POST /api/connections/{platform}/token", s.importToken)
	mux.HandleFunc("DELETE /api/connections/{platform}", s.disconnect)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.saveSettings)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})

	mux.HandleFunc("GET /media/{kind}/{name}", s.media)
	mux.HandleFunc("GET /oauth/{platform}/callback", s.oauthCallback)
	if s.MCP != nil {
		mux.Handle("/mcp", s.MCP)
	}
	mux.HandleFunc("/", s.web)

	csrf := http.NewCrossOriginProtection()
	return s.logRequests(localHostOnly(s.App.Cfg.BaseURL, csrf.Handler(mux)))
}

// PublicHandler is what the tunnel exposes: OAuth callbacks and signed,
// expiring links to converted videos (for platforms that fetch media).
func (s *Server) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /oauth/{platform}/callback", s.oauthCallback)
	mux.HandleFunc("GET /public-media/{token}", s.publicMedia)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	return s.logRequests(mux)
}

// ---- handlers ----

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.App.Status())
}

func (s *Server) listPosts(w http.ResponseWriter, r *http.Request) {
	posts, err := s.App.ListPosts(r.Context(), r.URL.Query().Get("status"), r.URL.Query().Get("platform"))
	s.respond(w, posts, err)
}

func (s *Server) createPost(w http.ResponseWriter, r *http.Request) {
	var in app.CreatePostInput
	if !decode(w, r, &in) {
		return
	}
	in.CreatedBy = "ui"
	v, err := s.App.CreatePost(r.Context(), in)
	s.respondStatus(w, http.StatusCreated, v, err)
}

func (s *Server) getPost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.App.GetPost(r.Context(), id)
	s.respond(w, v, err)
}

func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var patch app.PostPatch
	if !decode(w, r, &patch) {
		return
	}
	v, err := s.App.UpdatePost(r.Context(), id, patch)
	s.respond(w, v, err)
}

func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.App.DeletePost(r.Context(), id)
	s.respondStatus(w, http.StatusOK, map[string]bool{"deleted": err == nil}, err)
}

func (s *Server) updateTarget(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var patch app.TargetPatch
	if !decode(w, r, &patch) {
		return
	}
	v, err := s.App.UpdateTarget(r.Context(), id, r.PathValue("platform"), patch)
	s.respond(w, v, err)
}

// uploadMedia streams a multipart "file" field to disk without buffering it in memory.
func (s *Server) uploadMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data with a \"file\" field")
		return
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "upload interrupted: "+err.Error())
			return
		}
		if part.FormName() != "file" {
			continue
		}
		v, err := s.App.AttachUpload(r.Context(), id, part, part.FileName())
		s.respond(w, v, err)
		return
	}
	writeError(w, http.StatusBadRequest, "missing \"file\" field")
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.App.Approve(r.Context(), id)
	s.respond(w, v, err)
}

func (s *Server) unapprove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.App.Unapprove(r.Context(), id)
	s.respond(w, v, err)
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Platforms []string `json:"platforms"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	v, err := s.App.Publish(r.Context(), id, in.Platforms, false)
	s.respond(w, v, err)
}

func (s *Server) attempts(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := s.App.Store.ListAttempts(r.Context(), id)
	s.respond(w, list, err)
}

func (s *Server) platforms(w http.ResponseWriter, r *http.Request) {
	list, err := s.App.Platforms(r.Context())
	s.respond(w, list, err)
}

func (s *Server) accountInfo(w http.ResponseWriter, r *http.Request) {
	info, err := s.App.AccountInfo(r.Context(), r.PathValue("platform"))
	s.respond(w, map[string]any{"info": info}, err)
}

func (s *Server) startConnect(w http.ResponseWriter, r *http.Request) {
	u, err := s.App.StartConnect(r.Context(), r.PathValue("platform"))
	s.respond(w, map[string]string{"url": u}, err)
}

func (s *Server) importToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccessToken string `json:"accessToken"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.App.ImportToken(r.Context(), r.PathValue("platform"), in.AccessToken)
	s.respond(w, map[string]bool{"connected": err == nil}, err)
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	err := s.App.Disconnect(r.Context(), r.PathValue("platform"))
	s.respond(w, map[string]bool{"disconnected": err == nil}, err)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.App.GetSettings(r.Context())
	s.respond(w, st, err)
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	var in app.Settings
	if !decode(w, r, &in) {
		return
	}
	st, err := s.App.SaveSettings(r.Context(), in)
	s.respond(w, st, err)
}

func (s *Server) duplicate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.App.DuplicatePost(r.Context(), id)
	s.respondStatus(w, http.StatusCreated, v, err)
}

func (s *Server) checkLive(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.App.CheckLive(r.Context(), id)
	s.respond(w, v, err)
}

// uploadCover streams a multipart "file" image and sets it as custom cover.
func (s *Server) uploadCover(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data with a \"file\" field")
		return
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing \"file\" field")
			return
		}
		if part.FormName() == "file" {
			v, err := s.App.SetCoverImage(r.Context(), id, io.LimitReader(part, 20<<20), part.FileName())
			s.respond(w, v, err)
			return
		}
	}
}

func (s *Server) clearCover(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.App.ClearCoverImage(r.Context(), id)
	s.respond(w, v, err)
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("kind") == "covers" {
		p, err := s.App.CoverFile(r.Context(), r.PathValue("name"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeFile(w, r, p)
		return
	}
	p, ok := s.App.MediaFile(r.PathValue("kind"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, p) // supports Range requests for video seeking
}

// publicMedia serves a rendition to a platform via a signed, expiring token.
func (s *Server) publicMedia(w http.ResponseWriter, r *http.Request) {
	p, ok := s.App.ResolvePublicMedia(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, p)
}

// oauthCallback finishes a platform sign-in and sends the browser back to Settings.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	platformID := r.PathValue("platform")
	q := r.URL.Query()
	back := s.App.Cfg.BaseURL + "/settings"
	if e := q.Get("error"); e != "" {
		msg := e
		if d := q.Get("error_description"); d != "" {
			msg += ": " + d
		}
		redirectResult(w, r, back, platformID, msg)
		return
	}
	if err := s.App.FinishConnect(r.Context(), platformID, q.Get("code"), q.Get("state")); err != nil {
		s.Log.Warn("oauth callback", "platform", platformID, "err", err)
		redirectResult(w, r, back, platformID, err.Error())
		return
	}
	redirectResult(w, r, back, platformID, "")
}

func redirectResult(w http.ResponseWriter, r *http.Request, back, platformID, errMsg string) {
	q := url.Values{"connected": {platformID}}
	if errMsg != "" {
		q = url.Values{"connect_error": {platformID + ": " + errMsg}}
	}
	http.Redirect(w, r, back+"?"+q.Encode(), http.StatusFound)
}

// web serves the single-page UI with index.html as fallback for client routes.
func (s *Server) web(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.Web == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>PowerPoster</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>PowerPoster API is running</h1>
<p>The web UI is not built. Either run <code>npm --prefix web run build</code> and restart,
or start the dev UI with <code>npm --prefix web run dev</code> and open <a href="http://localhost:5173">http://localhost:5173</a>.</p>
<p>MCP endpoint: <code>%s/mcp</code></p></body>`, html.EscapeString(s.App.Cfg.BaseURL))
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	f, err := s.Web.Open(name)
	if err != nil {
		name = "index.html" // client-side route
		f, err = s.Web.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	io.Copy(w, f)
}

// ---- helpers ----

func (s *Server) respond(w http.ResponseWriter, v any, err error) {
	s.respondStatus(w, http.StatusOK, v, err)
}

func (s *Server) respondStatus(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		switch app.KindOf(err) {
		case app.KindInvalid:
			writeError(w, http.StatusBadRequest, err.Error())
		case app.KindNotFound:
			writeError(w, http.StatusNotFound, err.Error())
		case app.KindConflict:
			writeError(w, http.StatusConflict, err.Error())
		case app.KindForbidden:
			writeError(w, http.StatusForbidden, err.Error())
		default:
			s.Log.Error("request failed", "err", err)
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, status, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid post id")
		return 0, false
	}
	return id, true
}

// localHostOnly rejects requests whose Host header isn't a local name. This
// blocks DNS-rebinding attacks from web pages against the private listener.
func localHostOnly(baseURL string, next http.Handler) http.Handler {
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "powerposter": true, "host.docker.internal": true}
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		allowed[u.Hostname()] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if !allowed[strings.ToLower(host)] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming (MCP over SSE) working through the recorder.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/oauth/") || r.URL.Path == "/mcp" {
			if r.Method == http.MethodGet && rec.status < 400 && strings.HasPrefix(r.URL.Path, "/api/") {
				return // polling noise
			}
			s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "took", time.Since(start).Round(time.Millisecond))
		}
	})
}
