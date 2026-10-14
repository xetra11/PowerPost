// Package config loads PowerPoster settings from environment variables
// (and an optional .env file for native runs).
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	DataDir       string // database, media files, secret key fallback
	InboxDir      string // drop folder as seen by this process
	InboxHostPath string // the same folder as seen by the host (Claude) — for path mapping
	WebDir        string // built UI to serve when not embedded

	PrivateAddr   string // UI + API + MCP
	PublicAddr    string // OAuth callbacks only (reached through the tunnel)
	BaseURL       string // how the user's browser reaches the private listener
	PublicBaseURL string // https URL of the tunnel, used for OAuth redirect URIs

	NgrokDomain    string // e.g. name.ngrok-free.dev; sets PublicBaseURL if that is empty
	NgrokAutostart bool   // start the ngrok agent with the server (native runs)
	NgrokPath      string

	SecretKey string // base64, 32 bytes

	LegalOperator string // who runs this installation, named on /terms and /privacy
	LegalContact  string // contact e-mail shown on /terms and /privacy

	InstagramAppID      string
	InstagramAppSecret  string
	InstagramAPIVersion string
	TikTokClientKey     string
	TikTokClientSecret  string
	YouTubeClientID     string
	YouTubeClientSecret string

	DiscordMaxUploadMB int // upload limit of the Discord server(s): 10, 50 or 100

	FFmpegPath         string
	FFprobePath        string
	FFmpegEncoder      string // libx264 | h264_nvenc
	MaxParallelEncodes int
}

// Load reads the configuration. Values from the process environment win over .env.
func Load() (Config, error) {
	loadDotEnv(".env")

	c := Config{
		DataDir:             env("PP_DATA_DIR", "./data"),
		InboxDir:            env("PP_INBOX_DIR", "./inbox"),
		InboxHostPath:       env("PP_INBOX_HOST_PATH", ""),
		WebDir:              env("PP_WEB_DIR", "./internal/webui/dist"),
		PrivateAddr:         env("PP_PRIVATE_ADDR", "127.0.0.1:4000"),
		PublicAddr:          env("PP_PUBLIC_ADDR", "127.0.0.1:4001"),
		BaseURL:             strings.TrimRight(env("PP_BASE_URL", "http://127.0.0.1:4000"), "/"),
		PublicBaseURL:       strings.TrimRight(env("PUBLIC_BASE_URL", ""), "/"),
		NgrokDomain:         strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(env("NGROK_DOMAIN", ""), "https://"), "http://"), "/"),
		NgrokAutostart:      env("NGROK_AUTOSTART", "true") == "true",
		NgrokPath:           env("NGROK_PATH", "ngrok"),
		SecretKey:           env("POWERPOSTER_SECRET_KEY", ""),
		LegalOperator:       env("PP_LEGAL_OPERATOR", ""),
		LegalContact:        env("PP_LEGAL_CONTACT", ""),
		InstagramAppID:      env("INSTAGRAM_APP_ID", ""),
		InstagramAppSecret:  env("INSTAGRAM_APP_SECRET", ""),
		InstagramAPIVersion: env("INSTAGRAM_API_VERSION", "v23.0"),
		TikTokClientKey:     env("TIKTOK_CLIENT_KEY", ""),
		TikTokClientSecret:  env("TIKTOK_CLIENT_SECRET", ""),
		YouTubeClientID:     env("YOUTUBE_CLIENT_ID", ""),
		YouTubeClientSecret: env("YOUTUBE_CLIENT_SECRET", ""),
		FFmpegPath:          env("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:         env("FFPROBE_PATH", "ffprobe"),
		FFmpegEncoder:       env("FFMPEG_ENCODER", "libx264"),
	}

	if c.PublicBaseURL == "" && c.NgrokDomain != "" {
		c.PublicBaseURL = "https://" + c.NgrokDomain
	}

	n, err := strconv.Atoi(env("MAX_PARALLEL_ENCODES", "1"))
	if err != nil || n < 1 {
		return c, fmt.Errorf("MAX_PARALLEL_ENCODES must be a positive number")
	}
	c.MaxParallelEncodes = n

	mb, err := strconv.Atoi(env("DISCORD_MAX_UPLOAD_MB", "10"))
	if err != nil || mb < 1 {
		return c, fmt.Errorf("DISCORD_MAX_UPLOAD_MB must be a positive number (10, 50 or 100)")
	}
	c.DiscordMaxUploadMB = mb

	switch c.FFmpegEncoder {
	case "libx264", "h264_nvenc":
	default:
		return c, fmt.Errorf("FFMPEG_ENCODER must be libx264 or h264_nvenc, got %q", c.FFmpegEncoder)
	}

	for _, p := range []*string{&c.DataDir, &c.InboxDir, &c.WebDir} {
		abs, err := filepath.Abs(*p)
		if err != nil {
			return c, err
		}
		*p = abs
	}
	if c.InboxHostPath == "" {
		// Native run: host and process see the same folder.
		c.InboxHostPath = c.InboxDir
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// loadDotEnv sets variables from a KEY=VALUE file without overriding existing ones.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Strip inline comments on unquoted values ("FOO=bar   # note").
		if i := strings.Index(val, " #"); i >= 0 && !strings.HasPrefix(val, `"`) {
			val = strings.TrimSpace(val[:i])
		}
		val = strings.Trim(val, `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}
