// Package tunnel runs the ngrok agent next to the server, so the OAuth
// callback listener gets a public HTTPS URL without starting ngrok by hand.
package tunnel

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Ngrok keeps an ngrok agent running that forwards https://Domain to Target.
type Ngrok struct {
	Path   string // ngrok binary
	Domain string // e.g. name.ngrok-free.dev
	Target string // local address, e.g. 127.0.0.1:4001
	Log    *slog.Logger
}

// Args is the ngrok command line (without the binary).
func (n Ngrok) Args() []string {
	return []string{"http", n.Target, "--url", "https://" + n.Domain, "--log", "stdout", "--log-format", "logfmt"}
}

// Run starts ngrok and restarts it if it exits, until ctx is cancelled
// (which also stops the agent).
func (n Ngrok) Run(ctx context.Context) {
	if _, err := exec.LookPath(n.Path); err != nil {
		n.Log.Error("ngrok not found — install it (winget install ngrok.ngrok) or set NGROK_AUTOSTART=false", "path", n.Path)
		return
	}
	delay := 5 * time.Second
	for {
		start := time.Now()
		n.Log.Info("starting ngrok", "url", "https://"+n.Domain, "target", n.Target)
		err := n.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			delay = 5 * time.Second // it ran fine for a while; retry quickly
		}
		n.Log.Warn("ngrok stopped, restarting", "err", err, "in", delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, 5*time.Minute)
	}
}

func (n Ngrok) runOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, n.Path, n.Args()...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	n.logOutput(out)
	return cmd.Wait()
}

// logOutput forwards the agent's important lines and adds hints for common errors.
func (n Ngrok) logOutput(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "started tunnel"):
			n.Log.Info("ngrok tunnel online", "url", "https://"+n.Domain)
		case strings.Contains(line, "ERR_NGROK_334"), strings.Contains(line, "already online"):
			n.Log.Error("ngrok: this domain is already in use — close any ngrok window you started by hand", "detail", line)
		case strings.Contains(line, "ERR_NGROK_4018"), strings.Contains(line, "authtoken"):
			n.Log.Error("ngrok: missing or invalid authtoken — run: ngrok config add-authtoken <token>", "detail", line)
		case strings.Contains(line, "lvl=eror"), strings.Contains(line, "lvl=crit"), strings.Contains(line, "ERROR"):
			n.Log.Error("ngrok", "detail", line)
		}
	}
}
