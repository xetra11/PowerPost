// PowerPoster: compose one post, convert it for every platform, review it, publish it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"powerposter/internal/api"
	"powerposter/internal/app"
	"powerposter/internal/config"
	"powerposter/internal/mcpserver"
	"powerposter/internal/media"
	"powerposter/internal/platform"
	"powerposter/internal/platform/instagram"
	"powerposter/internal/platform/tiktok"
	"powerposter/internal/secret"
	"powerposter/internal/store"
	"powerposter/internal/tunnel"
	"powerposter/internal/webui"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "keygen":
		fmt.Println(secret.NewKey())
	case "healthcheck":
		err = healthcheck()
	default:
		fmt.Fprintf(os.Stderr, "usage: powerposter [serve|keygen|healthcheck]\n")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func serve() error {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	box, created, err := secret.LoadOrCreate(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		return err
	}
	if created {
		log.Warn("POWERPOSTER_SECRET_KEY not set: generated " + filepath.Join(cfg.DataDir, "secret.key") + ". Keep it with your data folder, or set the variable.")
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "powerposter.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	runner := media.Runner{FFmpeg: cfg.FFmpegPath, FFprobe: cfg.FFprobePath}
	if err := runner.Available(); err != nil {
		log.Error("FFmpeg is required for converting videos — install it or set FFMPEG_PATH/FFPROBE_PATH", "err", err)
	}

	reg := platform.NewRegistry(
		instagram.New(cfg.InstagramAppID, cfg.InstagramAppSecret, cfg.InstagramAPIVersion),
		tiktok.New(cfg.TikTokClientKey, cfg.TikTokClientSecret),
	)
	a := app.New(cfg, st, reg, runner, box, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Init(ctx); err != nil {
		return err
	}

	srv := &api.Server{App: a, Web: webFS(cfg, log), MCP: mcpserver.Handler(a), Log: log}
	private := &http.Server{Addr: cfg.PrivateAddr, Handler: srv.PrivateHandler(), ReadHeaderTimeout: 10 * time.Second}
	public := &http.Server{Addr: cfg.PublicAddr, Handler: srv.PublicHandler(), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 2)
	for _, s := range []*http.Server{private, public} {
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("listen %s: %w", s.Addr, err)
			}
		}()
	}
	tunnelDone := make(chan struct{})
	if cfg.NgrokDomain != "" && cfg.NgrokAutostart {
		target := cfg.PublicAddr
		if strings.HasPrefix(target, ":") {
			target = "127.0.0.1" + target
		}
		go func() {
			tunnel.Ngrok{Path: cfg.NgrokPath, Domain: cfg.NgrokDomain, Target: target, Log: log}.Run(ctx)
			close(tunnelDone)
		}()
	} else {
		close(tunnelDone)
	}
	workersDone := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(workersDone)
	}()

	log.Info("PowerPoster is running", "ui", cfg.BaseURL, "mcp", cfg.BaseURL+"/mcp", "inbox", cfg.InboxHostPath)
	if cfg.PublicBaseURL != "" {
		log.Info("OAuth callbacks", "public", cfg.PublicBaseURL)
	}

	select {
	case <-ctx.Done():
	case err = <-errc:
		stop()
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = private.Shutdown(shutdownCtx)
	_ = public.Shutdown(shutdownCtx)
	<-tunnelDone
	select {
	case <-workersDone:
	case <-shutdownCtx.Done():
		log.Warn("background jobs did not stop in time")
	}
	return err
}

// webFS prefers the embedded UI, then a built UI on disk.
func webFS(cfg config.Config, log *slog.Logger) fs.FS {
	if f := webui.FS(); f != nil {
		return f
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "index.html")); err == nil {
		log.Info("serving web UI from disk", "dir", cfg.WebDir)
		return os.DirFS(cfg.WebDir)
	}
	log.Info("web UI not built; use the Vite dev server (npm --prefix web run dev)")
	return nil
}

func healthcheck() error {
	addr := os.Getenv("PP_PRIVATE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:4000"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
