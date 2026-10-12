// Package discord posts videos to a Discord channel through a channel webhook.
// There is no OAuth: the webhook URL (Server settings → Integrations → Webhooks)
// is the secret that grants posting to that one channel.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"powerposter/internal/media"
	"powerposter/internal/platform"
)

const ID platform.ID = "discord"

// DefaultMaxUploadMB is the upload limit of servers without boosts.
const DefaultMaxUploadMB = 10

type Adapter struct {
	APIBase     string // https://discord.com/api/v10
	MaxUploadMB int    // server upload limit: 10, 50 (boost level 2) or 100 (level 3)
	HTTP        *http.Client
}

func New(maxUploadMB int) *Adapter {
	if maxUploadMB <= 0 {
		maxUploadMB = DefaultMaxUploadMB
	}
	return &Adapter{
		APIBase:     "https://discord.com/api/v10",
		MaxUploadMB: maxUploadMB,
		HTTP:        &http.Client{Timeout: 15 * time.Minute},
	}
}

type Options struct {
	Mention    string `json:"mention"`     // none | here | everyone
	ThreadName string `json:"thread_name"` // forum channels: title of the new post
}

func (a *Adapter) ID() platform.ID     { return ID }
func (a *Adapter) DisplayName() string { return "Discord" }
func (a *Adapter) SupportedKinds() []platform.PostKind {
	return []platform.PostKind{platform.KindVideo}
}

// Configured is always true: webhooks need no app credentials.
func (a *Adapter) Configured() bool             { return true }
func (a *Adapter) Auth() platform.Authenticator { return noAuth{} }

// Constraints: Discord plays any H.264 MP4 inline; the real limit is the
// server's upload size, which the converter targets via the bitrate.
func (a *Adapter) Constraints(platform.PostKind) platform.Constraints {
	return platform.Constraints{
		Video: &media.VideoConstraints{
			Containers:         []string{"mp4", "mov"},
			VideoCodecs:        []string{"h264"},
			AudioCodecs:        []string{"aac"},
			MaxWidth:           1920,
			MaxHeight:          1920,
			TargetWidth:        1080,
			TargetHeight:       1920,
			AspectMin:          0.01,
			AspectMax:          10,
			FPSMin:             1,
			FPSMax:             60,
			MaxBytes:           int64(a.MaxUploadMB) << 20,
			AudioSampleRateMax: 48000,
		},
		Caption: platform.CaptionRules{MaxChars: 2000},
	}
}

func (a *Adapter) OptionsSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "mention": {
      "type": "string",
      "title": "Notify",
      "enum": ["none", "here", "everyone"],
      "x-enumLabels": {"none": "Nobody (no ping)", "here": "@here (members online)", "everyone": "@everyone"},
      "description": "Ping members of the channel. @mentions inside the caption never ping.",
      "default": "none"
    },
    "thread_name": {
      "type": "string",
      "title": "Forum post title",
      "description": "Only for forum channels (required there). Leave empty for text channels."
    }
  }
}`)
}

func (a *Adapter) DefaultOptions() json.RawMessage {
	return json.RawMessage(`{"mention":"none","thread_name":""}`)
}

func (a *Adapter) Validate(t platform.ResolvedTarget) []platform.ValidationIssue {
	issue := func(sev platform.Severity, code, msg string) platform.ValidationIssue {
		return platform.ValidationIssue{Platform: ID, Severity: sev, Scope: platform.ScopeContent, Code: code, Message: msg}
	}
	var o Options
	if err := json.Unmarshal(t.Options, &o); err != nil {
		return []platform.ValidationIssue{issue(platform.SeverityError, "OPTIONS_INVALID", "Discord options are not valid: "+err.Error())}
	}
	var out []platform.ValidationIssue
	switch o.Mention {
	case "", "none", "here", "everyone":
	default:
		out = append(out, issue(platform.SeverityError, "OPTIONS_INVALID", fmt.Sprintf("Unknown Discord notify option %q.", o.Mention)))
	}
	if n := utf8.RuneCountInString(o.ThreadName); n > 100 {
		out = append(out, issue(platform.SeverityError, "THREAD_NAME_TOO_LONG", fmt.Sprintf("Forum post title is %d characters; maximum is 100.", n)))
	}
	// The caption itself is checked by the app; this catches the ping pushing it over.
	if n := utf8.RuneCountInString(content(t.Caption, o)); n > 2000 && utf8.RuneCountInString(t.Caption) <= 2000 {
		out = append(out, issue(platform.SeverityError, "CAPTION_TOO_LONG", fmt.Sprintf("Message is %d characters including the ping; maximum is 2000.", n)))
	}
	// Bits per second of the whole file; low values mean the size limit squeezed it.
	if t.File.Duration > 0 && t.File.Size > 0 {
		if kbps := float64(t.File.Size) * 8 / t.File.Duration / 1000; kbps < 1500 {
			out = append(out, issue(platform.SeverityWarning, "LOW_QUALITY",
				fmt.Sprintf("The video had to be compressed to about %.0f kbps to fit Discord's %d MB upload limit and may look blurry. Shorten it, or raise DISCORD_MAX_UPLOAD_MB if your server is boosted.", kbps, a.MaxUploadMB)))
		}
	}
	return out
}

func (a *Adapter) ReviewNotices(options json.RawMessage) []string {
	var o Options
	_ = json.Unmarshal(options, &o)
	if o.Mention == "here" || o.Mention == "everyone" {
		return []string{"This message pings @" + o.Mention + " in the Discord channel."}
	}
	return nil
}

// content is the message text: the optional ping, then the caption.
func content(caption string, o Options) string {
	if o.Mention == "here" || o.Mention == "everyone" {
		return "@" + o.Mention + "\n" + caption
	}
	return caption
}

// ---- Connecting ----

// TokenPrompt implements platform.TokenPrompter.
func (a *Adapter) TokenPrompt() platform.TokenPrompt {
	return platform.TokenPrompt{
		Label: "Webhook URL",
		Help:  "In Discord: right-click the channel → Edit Channel → Integrations → Webhooks → New Webhook → Copy Webhook URL. The webhook's name and avatar are what the posts show as author.",
		Only:  true,
	}
}

var webhookRe = regexp.MustCompile(`^https://(?:(?:canary|ptb)\.)?discord(?:app)?\.com/api(?:/v\d+)?/webhooks/(\d+)/([\w-]+)/?(?:\?.*)?$`)

type hook struct{ id, token string }

func parseWebhook(raw string) (hook, error) {
	m := webhookRe.FindStringSubmatch(raw)
	if m == nil {
		return hook{}, errors.New("this is not a Discord webhook URL (https://discord.com/api/webhooks/<id>/<token>)")
	}
	return hook{m[1], m[2]}, nil
}

func (h hook) path() string { return "/webhooks/" + h.id + "/" + h.token }

// redact hides the webhook token, which transport errors include as part of the URL.
func (h hook) redact(err error) error {
	if err == nil || !strings.Contains(err.Error(), h.token) {
		return err
	}
	return redacted{msg: strings.ReplaceAll(err.Error(), h.token, "***"), err: err}
}

type redacted struct {
	msg string
	err error
}

func (r redacted) Error() string { return r.msg }
func (r redacted) Unwrap() error { return r.err }

type webhookInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
}

// ImportToken implements platform.TokenImporter: the "token" is the webhook URL.
func (a *Adapter) ImportToken(ctx context.Context, webhookURL string) (platform.TokenSet, error) {
	h, err := parseWebhook(webhookURL)
	if err != nil {
		return platform.TokenSet{}, err
	}
	info, err := a.webhook(ctx, h)
	if err != nil {
		return platform.TokenSet{}, fmt.Errorf("Discord rejected the webhook: %w", err)
	}
	return platform.TokenSet{
		AccessToken: webhookURL,
		AccountID:   info.ChannelID,
		AccountName: info.Name,
	}, nil
}

func (a *Adapter) webhook(ctx context.Context, h hook) (webhookInfo, error) {
	var info webhookInfo
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.APIBase+h.path(), nil)
	err := platform.DoJSON(a.HTTP, req, &info, discordError)
	return info, h.redact(err)
}

// noAuth satisfies platform.Authenticator; Discord connects by webhook URL only.
type noAuth struct{}

func (noAuth) AuthorizeURL(string, string, string) string { return "" }
func (noAuth) Exchange(context.Context, string, string, string) (platform.TokenSet, error) {
	return platform.TokenSet{}, errors.New("Discord connects with a webhook URL, not a sign-in")
}
func (noAuth) Refresh(_ context.Context, t platform.TokenSet) (platform.TokenSet, error) {
	return t, nil
}
func (noAuth) RefreshMargin() time.Duration { return 0 }

// ---- Publish ----

type message struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Reactions []struct {
		Count int64 `json:"count"`
	} `json:"reactions"`
	Thread *struct {
		MessageCount int64 `json:"message_count"`
	} `json:"thread"`
}

func (a *Adapter) Publish(ctx context.Context, pc platform.PublishContext, t platform.ResolvedTarget) (platform.PublishResult, error) {
	var o Options
	if err := json.Unmarshal(t.Options, &o); err != nil {
		return platform.PublishResult{}, err
	}
	h, err := parseWebhook(pc.Token.AccessToken)
	if err != nil {
		return platform.PublishResult{}, err
	}

	pc.Progress("checking webhook")
	info, err := a.webhook(ctx, h)
	if err != nil {
		return platform.PublishResult{}, fmt.Errorf("webhook: %w (was it deleted? Paste a new webhook URL in Settings)", err)
	}

	f, err := os.Open(t.FilePath)
	if err != nil {
		return platform.PublishResult{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return platform.PublishResult{}, err
	}
	if limit := int64(a.MaxUploadMB) << 20; st.Size() > limit {
		return platform.PublishResult{}, fmt.Errorf("video is %s; the Discord upload limit is %s", media.FormatBytes(st.Size()), media.FormatBytes(limit))
	}

	parse := []string{"users", "roles"}
	if o.Mention == "here" || o.Mention == "everyone" {
		parse = append(parse, "everyone") // covers @here too
	}
	filename := fmt.Sprintf("post-%d.mp4", t.PostID)
	payload := map[string]any{
		"content":          content(t.Caption, o),
		"allowed_mentions": map[string]any{"parse": parse},
		"attachments":      []map[string]any{{"id": 0, "filename": filename}},
	}
	if o.ThreadName != "" {
		payload["thread_name"] = o.ThreadName
	}

	pc.Progress("uploading")
	pc.Log("uploading %s to webhook %q", media.FormatBytes(st.Size()), info.Name)
	var msg message
	for attempt := 0; ; attempt++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return platform.PublishResult{}, err
		}
		err = a.execute(ctx, h, payload, filename, f, st.Size(), &msg)
		var rl *rateLimited
		if !errors.As(err, &rl) || attempt == 2 {
			break
		}
		pc.Log("rate limited, retrying in %s", rl.wait)
		select {
		case <-ctx.Done():
			return platform.PublishResult{}, ctx.Err()
		case <-time.After(rl.wait):
		}
	}
	if err != nil {
		var apiErr *platform.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusRequestEntityTooLarge {
			return platform.PublishResult{}, fmt.Errorf("Discord refused the %s file as too large for this server; lower DISCORD_MAX_UPLOAD_MB: %w", media.FormatBytes(st.Size()), err)
		}
		return platform.PublishResult{}, fmt.Errorf("upload: %w", err)
	}

	res := platform.PublishResult{RemoteID: msg.ID}
	if msg.ChannelID != "" && msg.ChannelID != info.ChannelID {
		res.RemoteID = msg.ChannelID + "/" + msg.ID // posted into a (forum) thread
	}
	if info.GuildID != "" {
		res.Permalink = fmt.Sprintf("https://discord.com/channels/%s/%s/%s", info.GuildID, msg.ChannelID, msg.ID)
	}
	return res, nil
}

type rateLimited struct{ wait time.Duration }

func (r *rateLimited) Error() string { return "rate limited" }

// execute sends the multipart webhook request, streaming the file.
func (a *Adapter) execute(ctx context.Context, h hook, payload map[string]any, filename string, file io.Reader, size int64, out *message) error {
	pj, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	if err := mw.WriteField("payload_json", string(pj)); err != nil {
		return err
	}
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[0]"; filename=%q`, filepath.Base(filename)))
	hdr.Set("Content-Type", "video/mp4")
	if _, err := mw.CreatePart(hdr); err != nil {
		return err
	}
	tail := "\r\n--" + mw.Boundary() + "--\r\n"

	body := io.MultiReader(bytes.NewReader(head.Bytes()), file, bytes.NewReader([]byte(tail)))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.APIBase+h.path()+"?wait=true", body)
	req.ContentLength = int64(head.Len()) + size + int64(len(tail))
	req.Header.Set("Content-Type", mw.FormDataContentType())

	err = h.redact(platform.DoJSON(a.HTTP, req, out, discordError))
	var apiErr *platform.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests {
		var r struct {
			RetryAfter float64 `json:"retry_after"`
		}
		_ = json.Unmarshal([]byte(apiErr.Body), &r)
		return &rateLimited{wait: time.Duration(max(r.RetryAfter, 1) * float64(time.Second))}
	}
	return err
}

// message reads a message the webhook posted; remoteID is "<id>" or "<thread>/<id>".
func (a *Adapter) message(ctx context.Context, t platform.TokenSet, remoteID string) (message, error) {
	var m message
	h, err := parseWebhook(t.AccessToken)
	if err != nil {
		return m, err
	}
	u := a.APIBase + h.path() + "/messages/"
	if thread, id, ok := strings.Cut(remoteID, "/"); ok {
		u += url.PathEscape(id) + "?thread_id=" + url.QueryEscape(thread)
	} else {
		u += url.PathEscape(remoteID)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	err = platform.DoJSON(a.HTTP, req, &m, discordError)
	return m, h.redact(err)
}

// IsLive implements platform.LiveChecker: a deleted message answers 404.
func (a *Adapter) IsLive(ctx context.Context, t platform.TokenSet, remoteID string) (bool, error) {
	_, err := a.message(ctx, t, remoteID)
	if err == nil {
		return true, nil
	}
	var apiErr *platform.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return false, nil
	}
	return false, err
}

// Stats implements platform.StatsProvider: reactions count as likes,
// replies in a thread started from the message as comments.
func (a *Adapter) Stats(ctx context.Context, t platform.TokenSet, remoteID string) (platform.Stats, error) {
	s := platform.Stats{At: time.Now(), Note: "Likes are all emoji reactions on the message."}
	m, err := a.message(ctx, t, remoteID)
	if err != nil {
		return s, err
	}
	var likes int64
	for _, r := range m.Reactions {
		likes += r.Count
	}
	s.Likes = &likes
	if m.Thread != nil {
		c := m.Thread.MessageCount
		s.Comments = &c
	}
	return s, nil
}

func discordError(body []byte) string {
	var e struct {
		Message string          `json:"message"`
		Code    int             `json:"code"`
		Errors  json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil || e.Message == "" {
		return ""
	}
	msg := e.Message + " (code " + strconv.Itoa(e.Code) + ")"
	if len(e.Errors) > 0 {
		msg += ": " + string(e.Errors)
	}
	return msg
}
