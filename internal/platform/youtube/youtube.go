// Package youtube publishes YouTube Shorts via the YouTube Data API v3.
//
// There is no separate Shorts API: a vertical (or square) video of at most
// three minutes uploaded with videos.insert becomes a Short automatically.
package youtube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"powerposter/internal/media"
	"powerposter/internal/platform"
)

const ID platform.ID = "youtube"

const (
	scopeUpload   = "https://www.googleapis.com/auth/youtube.upload"
	scopeReadonly = "https://www.googleapis.com/auth/youtube.readonly"

	maxTitle = 100
)

var privacyStatuses = []string{"public", "unlisted", "private"}

type Adapter struct {
	ClientID     string
	ClientSecret string

	AuthBase   string // https://accounts.google.com
	TokenURL   string // https://oauth2.googleapis.com/token
	APIBase    string // https://www.googleapis.com/youtube/v3
	UploadBase string // https://www.googleapis.com/upload/youtube/v3

	HTTP         *http.Client
	ChunkSize    int64 // a multiple of 256 KiB, except in tests
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func New(clientID, clientSecret string) *Adapter {
	return &Adapter{
		ClientID: clientID, ClientSecret: clientSecret,
		AuthBase:   "https://accounts.google.com",
		TokenURL:   "https://oauth2.googleapis.com/token",
		APIBase:    "https://www.googleapis.com/youtube/v3",
		UploadBase: "https://www.googleapis.com/upload/youtube/v3",
		HTTP: &http.Client{
			Timeout: 30 * time.Minute,
			// Resumable uploads answer unfinished chunks with 308; that is not a redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		ChunkSize:    8 << 20,
		PollInterval: 5 * time.Second,
		PollTimeout:  10 * time.Minute,
	}
}

type Options struct {
	Title             string `json:"title"` // empty: first line of the caption
	PrivacyStatus     string `json:"privacy_status"`
	MadeForKids       bool   `json:"made_for_kids"`
	CategoryID        string `json:"category_id"`
	NotifySubscribers bool   `json:"notify_subscribers"`
	SyntheticMedia    bool   `json:"contains_synthetic_media"`
}

func (a *Adapter) ID() platform.ID     { return ID }
func (a *Adapter) DisplayName() string { return "YouTube" }
func (a *Adapter) SupportedKinds() []platform.PostKind {
	return []platform.PostKind{platform.KindVideo}
}
func (a *Adapter) Configured() bool             { return a.ClientID != "" && a.ClientSecret != "" }
func (a *Adapter) Auth() platform.Authenticator { return a }

// Constraints follow https://support.google.com/youtube/answer/15424877 (Shorts:
// vertical or square, up to 3 minutes). YouTube takes almost any format; we
// deliver the same 1080x1920 H.264 MP4 as for TikTok and Reels.
// The description limit is 5000 bytes; counting characters is close enough
// for mostly ASCII captions, and YouTube ignores all hashtags above 60.
func (a *Adapter) Constraints(kind platform.PostKind) platform.Constraints {
	if kind != platform.KindVideo {
		return platform.Constraints{}
	}
	return platform.Constraints{
		Video: &media.VideoConstraints{
			Containers:   []string{"mp4", "mov"},
			VideoCodecs:  []string{"h264", "hevc"},
			AudioCodecs:  []string{"aac", "mp3"},
			MaxWidth:     3840,
			MaxHeight:    3840,
			TargetWidth:  1080,
			TargetHeight: 1920,
			AspectMax:    1, // wider videos are no Shorts
			FPSMin:       23,
			FPSMax:       60,
			DurationMin:  1,
			DurationMax:  3 * 60,
			MaxBytes:     256 << 30,
		},
		Caption: platform.CaptionRules{MaxChars: 5000, MaxHashtags: 60},
	}
}

func (a *Adapter) OptionsSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "title": {
      "type": "string",
      "title": "Title",
      "maxLength": 100,
      "description": "Up to 100 characters. Leave empty to use the first line of the caption. The caption becomes the description."
    },
    "privacy_status": {
      "type": "string",
      "title": "Visibility",
      "enum": ["public", "unlisted", "private"],
      "x-enumLabels": {"public": "Public", "unlisted": "Unlisted", "private": "Private"},
      "description": "Until your Google Cloud project passes the YouTube API audit, YouTube locks every upload as private.",
      "default": "public"
    },
    "made_for_kids": {
      "type": "boolean",
      "title": "Made for kids",
      "description": "Required by law (COPPA). Made-for-kids videos have comments and some features turned off.",
      "default": false
    },
    "category_id": {
      "type": "string",
      "title": "Category",
      "enum": ["20", "24", "1", "22", "28"],
      "x-enumLabels": {"20": "Gaming", "24": "Entertainment", "1": "Film & Animation", "22": "People & Blogs", "28": "Science & Technology"},
      "default": "20"
    },
    "notify_subscribers": {"type": "boolean", "title": "Notify subscribers", "default": true},
    "contains_synthetic_media": {
      "type": "boolean",
      "title": "Altered or synthetic content",
      "description": "Realistic content made or changed with AI or other tools that viewers could mistake for real. YouTube shows a label.",
      "default": false
    }
  }
}`)
}

func (a *Adapter) DefaultOptions() json.RawMessage {
	return json.RawMessage(`{"title":"","privacy_status":"public","made_for_kids":false,"category_id":"20","notify_subscribers":true,"contains_synthetic_media":false}`)
}

// title is the explicit title, or the first line of the caption cut to 100 characters.
func (o Options) title(caption string) (title string, derived bool) {
	if t := strings.TrimSpace(o.Title); t != "" {
		return t, false
	}
	for _, line := range strings.Split(caption, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > maxTitle {
			line = strings.TrimSpace(string(r[:maxTitle-1])) + "…"
		}
		return line, true
	}
	return "", true
}

func (a *Adapter) Validate(t platform.ResolvedTarget) []platform.ValidationIssue {
	var out []platform.ValidationIssue
	add := func(sev platform.Severity, code, msg string) {
		out = append(out, platform.ValidationIssue{Platform: ID, Severity: sev, Scope: platform.ScopeContent, Code: code, Message: msg})
	}
	var o Options
	if err := json.Unmarshal(t.Options, &o); err != nil {
		add(platform.SeverityError, "OPTIONS_INVALID", "YouTube options are not valid: "+err.Error())
		return out
	}
	if !slices.Contains(privacyStatuses, o.PrivacyStatus) {
		add(platform.SeverityError, "PRIVACY_INVALID", `Visibility must be "public", "unlisted" or "private".`)
	}
	title, derived := o.title(t.Caption)
	switch {
	case title == "":
		add(platform.SeverityError, "TITLE_REQUIRED", "YouTube needs a title: set one in the YouTube options or write a caption.")
	case !derived && utf8.RuneCountInString(title) > maxTitle:
		add(platform.SeverityError, "TITLE_TOO_LONG", fmt.Sprintf("Title is %d characters; maximum is %d.", utf8.RuneCountInString(title), maxTitle))
	case derived:
		add(platform.SeverityInfo, "TITLE_FROM_CAPTION", fmt.Sprintf("Title: %q (first line of the caption). Set a title in the YouTube options to change it.", title))
	}
	if strings.ContainsAny(title, "<>") {
		add(platform.SeverityError, "TITLE_INVALID_CHARS", "YouTube doesn't accept < or > in the title.")
	}
	if strings.ContainsAny(t.Caption, "<>") {
		add(platform.SeverityError, "DESCRIPTION_INVALID_CHARS", "YouTube doesn't accept < or > in the description (the caption).")
	}
	if o.PrivacyStatus == "public" || o.PrivacyStatus == "unlisted" {
		add(platform.SeverityInfo, "AUDIT_NOTE", "Until your Google Cloud project passes the YouTube API audit, YouTube locks uploads as private.")
	}
	return out
}

func (a *Adapter) ReviewNotices(raw json.RawMessage) []string {
	n := []string{"By publishing, you agree to the YouTube Terms of Service (youtube.com/t/terms)."}
	var o Options
	if json.Unmarshal(raw, &o) == nil && o.MadeForKids {
		n = append(n, "The video is marked as made for kids: YouTube turns off comments and some features.")
	}
	return n
}

// ---- Auth ----

// AuthorizeURL asks for offline access (a refresh token) and forces the consent
// screen, because Google only returns a refresh token on the first consent.
func (a *Adapter) AuthorizeURL(state, redirectURI, verifier string) string {
	q := url.Values{
		"client_id":     {a.ClientID},
		"response_type": {"code"},
		"scope":         {scopeUpload + " " + scopeReadonly},
		"redirect_uri":  {redirectURI},
		"state":         {state},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
	}
	if verifier != "" {
		sum := sha256.Sum256([]byte(verifier))
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		q.Set("code_challenge_method", "S256")
	}
	return a.AuthBase + "/o/oauth2/v2/auth?" + q.Encode()
}

// RefreshMargin: Google access tokens live one hour.
func (a *Adapter) RefreshMargin() time.Duration { return 10 * time.Minute }

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func (a *Adapter) Exchange(ctx context.Context, code, redirectURI, verifier string) (platform.TokenSet, error) {
	form := url.Values{
		"client_id":     {a.ClientID},
		"client_secret": {a.ClientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	ts, err := a.token(ctx, form)
	if err != nil {
		return ts, fmt.Errorf("code exchange: %w", err)
	}
	granted := strings.Fields(ts.Scopes)
	if !slices.Contains(granted, scopeUpload) || !slices.Contains(granted, scopeReadonly) {
		return ts, fmt.Errorf("PowerPoster needs both permissions (upload videos and view your YouTube account). Connect again and leave both boxes checked")
	}
	if ts.RefreshToken == "" {
		return ts, fmt.Errorf("Google returned no refresh token. Remove PowerPoster at myaccount.google.com/permissions and connect again")
	}
	var ch struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := a.get(ctx, ts.AccessToken, "/channels", url.Values{"part": {"snippet"}, "mine": {"true"}}, &ch); err != nil {
		return ts, fmt.Errorf("read channel: %w", err)
	}
	if len(ch.Items) == 0 {
		return ts, fmt.Errorf("this Google account has no YouTube channel. Create one on youtube.com (or pick the brand account that owns the channel when signing in) and connect again")
	}
	ts.AccountID, ts.AccountName = ch.Items[0].ID, ch.Items[0].Snippet.Title
	return ts, nil
}

// Refresh renews the access token. Google keeps the refresh token (it has no
// fixed expiry) and usually doesn't send it again.
func (a *Adapter) Refresh(ctx context.Context, t platform.TokenSet) (platform.TokenSet, error) {
	ts, err := a.token(ctx, url.Values{
		"client_id":     {a.ClientID},
		"client_secret": {a.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	})
	if err != nil {
		return t, err
	}
	if ts.RefreshToken == "" {
		ts.RefreshToken = t.RefreshToken
	}
	if ts.Scopes == "" {
		ts.Scopes = t.Scopes
	}
	ts.AccountID, ts.AccountName = t.AccountID, t.AccountName
	return ts, nil
}

func (a *Adapter) token(ctx context.Context, form url.Values) (platform.TokenSet, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var r tokenResponse
	if err := platform.DoJSON(a.HTTP, req, &r, googleError); err != nil {
		return platform.TokenSet{}, err
	}
	if r.AccessToken == "" {
		return platform.TokenSet{}, fmt.Errorf("Google returned no access token")
	}
	return platform.TokenSet{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(r.ExpiresIn) * time.Second),
		Scopes:       r.Scope,
	}, nil
}

// ---- Publish ----

type video struct {
	ID     string `json:"id"`
	Status struct {
		UploadStatus    string `json:"uploadStatus"`
		FailureReason   string `json:"failureReason"`
		RejectionReason string `json:"rejectionReason"`
		PrivacyStatus   string `json:"privacyStatus"`
	} `json:"status"`
	ProcessingDetails struct {
		ProcessingStatus   string `json:"processingStatus"`
		ProcessingProgress struct {
			PartsTotal     string `json:"partsTotal"`
			PartsProcessed string `json:"partsProcessed"`
		} `json:"processingProgress"`
	} `json:"processingDetails"`
}

func (a *Adapter) Publish(ctx context.Context, pc platform.PublishContext, t platform.ResolvedTarget) (platform.PublishResult, error) {
	var o Options
	if err := json.Unmarshal(t.Options, &o); err != nil {
		return platform.PublishResult{}, err
	}
	title, _ := o.title(t.Caption)
	f, err := os.Open(t.FilePath)
	if err != nil {
		return platform.PublishResult{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return platform.PublishResult{}, err
	}
	token := pc.Token.AccessToken
	if pc.Token.AccountName != "" {
		pc.Log("uploading to channel %s", pc.Token.AccountName)
	}

	pc.Progress("initializing upload")
	meta := map[string]any{
		"snippet": map[string]any{"title": title, "description": t.Caption, "categoryId": o.CategoryID},
		"status": map[string]any{
			"privacyStatus":           o.PrivacyStatus,
			"selfDeclaredMadeForKids": o.MadeForKids,
			"containsSyntheticMedia":  o.SyntheticMedia,
		},
	}
	session, err := a.startUpload(ctx, token, meta, st.Size(), o.NotifySubscribers)
	if err != nil {
		return platform.PublishResult{}, fmt.Errorf("init upload: %w", err)
	}
	v, err := a.uploadChunks(ctx, pc, session, f, st.Size())
	if err != nil {
		return platform.PublishResult{}, fmt.Errorf("upload: %w", err)
	}
	pc.Log("video id %s, %d bytes", v.ID, st.Size())
	res := platform.PublishResult{RemoteID: v.ID, Permalink: "https://www.youtube.com/shorts/" + v.ID}

	pc.Progress("processing on YouTube")
	return a.waitProcessed(ctx, pc, token, res, o.PrivacyStatus)
}

// startUpload opens a resumable upload session and returns its URL.
func (a *Adapter) startUpload(ctx context.Context, token string, meta any, size int64, notify bool) (string, error) {
	b, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	q := url.Values{"uploadType": {"resumable"}, "part": {"snippet,status"}, "notifySubscribers": {strconv.FormatBool(notify)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.UploadBase+"/videos?"+q.Encode(), bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	req.Header.Set("X-Upload-Content-Type", "video/mp4")
	resp, body, err := a.do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", apiError(resp, body)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("YouTube returned no upload URL")
	}
	return loc, nil
}

// uploadChunks sends the file in ChunkSize pieces. YouTube answers 308 with the
// received byte range until the last chunk, then 200/201 with the video.
func (a *Adapter) uploadChunks(ctx context.Context, pc platform.PublishContext, session string, f *os.File, size int64) (video, error) {
	var v video
	offset := int64(0)
	for {
		end := min(offset+a.ChunkSize, size) - 1
		pc.Progress(fmt.Sprintf("uploading %d%%", offset*100/max(size, 1)))
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, session, io.NewSectionReader(f, offset, end-offset+1))
		req.ContentLength = end - offset + 1
		req.Header.Set("Content-Type", "video/mp4")
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, size))
		resp, body, err := a.do(req)
		if err != nil {
			return v, err
		}
		switch resp.StatusCode {
		case http.StatusOK, http.StatusCreated:
			if err := json.Unmarshal(body, &v); err != nil || v.ID == "" {
				return v, fmt.Errorf("unexpected upload response: %.300s", body)
			}
			return v, nil
		case http.StatusPermanentRedirect:
			// Range "bytes=0-N" is what YouTube has; continue after it.
			next := int64(0)
			if r := resp.Header.Get("Range"); r != "" {
				if _, last, ok := strings.Cut(r, "-"); ok {
					n, err := strconv.ParseInt(last, 10, 64)
					if err != nil {
						return v, fmt.Errorf("bad Range header %q", r)
					}
					next = n + 1
				}
			}
			if next <= offset {
				return v, fmt.Errorf("upload made no progress at byte %d", offset)
			}
			offset = next
		default:
			return v, apiError(resp, body)
		}
	}
}

// waitProcessed polls until YouTube finished processing. The video is already
// uploaded, so status problems end in a note, not an error, to avoid a
// duplicate upload on retry.
func (a *Adapter) waitProcessed(ctx context.Context, pc platform.PublishContext, token string, res platform.PublishResult, wantPrivacy string) (platform.PublishResult, error) {
	deadline := time.Now().Add(a.PollTimeout)
	last := ""
	for {
		var list struct {
			Items []video `json:"items"`
		}
		if err := a.get(ctx, token, "/videos", url.Values{"part": {"status,processingDetails"}, "id": {res.RemoteID}}, &list); err != nil {
			res.Note = fmt.Sprintf("Uploaded, but the processing status couldn't be read (%v). Check the video in YouTube Studio.", err)
			return res, nil
		}
		if len(list.Items) > 0 {
			v := list.Items[0]
			pd := v.ProcessingDetails
			cur := v.Status.UploadStatus
			if pd.ProcessingStatus != "" {
				cur += ", " + pd.ProcessingStatus
			}
			if p := pd.ProcessingProgress; p.PartsTotal != "" && p.PartsTotal != "0" {
				cur += fmt.Sprintf(" %s/%s", p.PartsProcessed, p.PartsTotal)
			}
			if cur != last {
				last = cur
				pc.Progress(fmt.Sprintf("processing on YouTube (%s)", cur))
			}
			switch {
			case v.Status.UploadStatus == "rejected":
				return res, fmt.Errorf("YouTube rejected the video: %s", v.Status.RejectionReason)
			case v.Status.UploadStatus == "failed":
				return res, fmt.Errorf("YouTube couldn't process the video: %s", v.Status.FailureReason)
			case v.Status.UploadStatus == "deleted":
				return res, fmt.Errorf("the video was deleted on YouTube")
			case pd.ProcessingStatus == "failed" || pd.ProcessingStatus == "terminated":
				return res, fmt.Errorf("YouTube processing %s", pd.ProcessingStatus)
			case v.Status.UploadStatus == "processed" || pd.ProcessingStatus == "succeeded":
				if wantPrivacy != "private" && v.Status.PrivacyStatus == "private" {
					res.Note = "YouTube locked the video as private: uploads from Google Cloud projects that haven't passed the YouTube API audit stay private (see README → YouTube)."
				}
				return res, nil
			}
		}
		if time.Now().After(deadline) {
			res.Note = "Uploaded. YouTube is still processing the video; it appears on the channel when it's done."
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(a.PollInterval):
		}
	}
}

// ---- Optional interfaces ----

// IsLive implements platform.LiveChecker: deleted videos vanish from videos.list.
func (a *Adapter) IsLive(ctx context.Context, t platform.TokenSet, remoteID string) (bool, error) {
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := a.get(ctx, t.AccessToken, "/videos", url.Values{"part": {"id"}, "id": {remoteID}}, &list); err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}

// Stats implements platform.StatsProvider. YouTube sends counts as strings and
// leaves out likes when the channel hides them.
func (a *Adapter) Stats(ctx context.Context, t platform.TokenSet, remoteID string) (platform.Stats, error) {
	s := platform.Stats{At: time.Now()}
	var list struct {
		Items []struct {
			Statistics map[string]string `json:"statistics"`
		} `json:"items"`
	}
	if err := a.get(ctx, t.AccessToken, "/videos", url.Values{"part": {"statistics"}, "id": {remoteID}}, &list); err != nil {
		return s, err
	}
	if len(list.Items) == 0 {
		return s, fmt.Errorf("video %s not found", remoteID)
	}
	num := func(key string) *int64 {
		n, err := strconv.ParseInt(list.Items[0].Statistics[key], 10, 64)
		if err != nil {
			return nil
		}
		return &n
	}
	s.Views, s.Likes, s.Comments = num("viewCount"), num("likeCount"), num("commentCount")
	return s, nil
}

var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// EmbedURL implements platform.Embedder (private videos show YouTube's "private" notice).
func (a *Adapter) EmbedURL(_, remoteID string) string {
	if !videoIDRe.MatchString(remoteID) {
		return ""
	}
	return "https://www.youtube.com/embed/" + remoteID
}

// MissingSettings implements platform.SetupReporter.
func (a *Adapter) MissingSettings() []string {
	var m []string
	if a.ClientID == "" {
		m = append(m, "YOUTUBE_CLIENT_ID")
	}
	if a.ClientSecret == "" {
		m = append(m, "YOUTUBE_CLIENT_SECRET")
	}
	return m
}

// AllowsLocalRedirect implements platform.LocalRedirector: Google accepts
// http://localhost redirect URIs for web application clients.
func (a *Adapter) AllowsLocalRedirect() bool { return true }

// ---- HTTP ----

func (a *Adapter) get(ctx context.Context, token, path string, q url.Values, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.APIBase+path+"?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return platform.DoJSON(a.HTTP, req, out, googleError)
}

// do sends req and returns the response with its body read.
func (a *Adapter) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp, body, err
}

func apiError(resp *http.Response, body []byte) error {
	s := strings.TrimSpace(string(body))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return &platform.APIError{Status: resp.StatusCode, Message: googleError(body), Body: s}
}

// googleError reads both Google error shapes: OAuth's {"error":"invalid_grant",
// "error_description":…} and the API's {"error":{"message":…,"errors":[{"reason":…}]}}.
func googleError(body []byte) string {
	var env struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	if json.Unmarshal(body, &env) != nil || len(env.Error) == 0 {
		return ""
	}
	var code string
	if json.Unmarshal(env.Error, &code) == nil {
		msg := code
		if env.Description != "" {
			msg += ": " + env.Description
		}
		if code == "invalid_grant" {
			msg += " (access was revoked or expired; reconnect YouTube in Settings)"
		}
		return msg
	}
	var e struct {
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if json.Unmarshal(env.Error, &e) != nil {
		return ""
	}
	reason := ""
	if len(e.Errors) > 0 {
		reason = e.Errors[0].Reason
	}
	msg := e.Message
	if reason != "" {
		msg = reason + ": " + msg
	}
	if hint := reasonHints[reason]; hint != "" {
		msg += " (" + hint + ")"
	}
	return msg
}

var reasonHints = map[string]string{
	"quotaExceeded":           "the project's daily YouTube API quota is used up; it resets at midnight Pacific time",
	"uploadLimitExceeded":     "the channel reached YouTube's daily upload limit",
	"youtubeSignupRequired":   "this Google account has no YouTube channel yet",
	"insufficientPermissions": "reconnect YouTube in Settings and allow all requested permissions",
}
