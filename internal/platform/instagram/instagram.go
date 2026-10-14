// Package instagram publishes Reels via the Instagram API with Instagram Login.
package instagram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"powerposter/internal/media"
	"powerposter/internal/platform"
)

const ID platform.ID = "instagram"

type Adapter struct {
	AppID     string
	AppSecret string
	Version   string // Graph API version, e.g. v23.0

	AuthBase   string // https://www.instagram.com
	TokenBase  string // https://api.instagram.com
	GraphBase  string // https://graph.instagram.com
	UploadBase string // https://rupload.facebook.com

	HTTP         *http.Client
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func New(appID, appSecret, version string) *Adapter {
	return &Adapter{
		AppID: appID, AppSecret: appSecret, Version: version,
		AuthBase:     "https://www.instagram.com",
		TokenBase:    "https://api.instagram.com",
		GraphBase:    "https://graph.instagram.com",
		UploadBase:   "https://rupload.facebook.com",
		HTTP:         &http.Client{Timeout: 30 * time.Minute},
		PollInterval: 5 * time.Second,
		PollTimeout:  15 * time.Minute,
	}
}

type Options struct {
	ShareToFeed bool `json:"share_to_feed"`
}

func (a *Adapter) ID() platform.ID     { return ID }
func (a *Adapter) DisplayName() string { return "Instagram" }
func (a *Adapter) SupportedKinds() []platform.PostKind {
	return []platform.PostKind{platform.KindVideo, platform.KindImage}
}
func (a *Adapter) Configured() bool             { return a.AppID != "" && a.AppSecret != "" }
func (a *Adapter) Auth() platform.Authenticator { return a }

// Constraints follow Meta's specs for API publishing (Reels; images and
// carousels). Verify against
// https://developers.facebook.com/docs/instagram-platform/instagram-graph-api/reference/ig-user/media
// when they change.
func (a *Adapter) Constraints(kind platform.PostKind) platform.Constraints {
	caption := platform.CaptionRules{MaxChars: 2200, MaxHashtags: 30, MaxMentions: 20}
	if kind == platform.KindImage {
		return platform.Constraints{
			Image: &media.ImageConstraints{
				Formats:        []string{"jpeg"},
				MaxWidth:       1080, // Instagram takes up to 1440 but shows 1080
				MaxHeight:      1350,
				AspectMin:      0.8, // 4:5 portrait
				AspectMax:      1.91,
				MaxBytes:       8 << 20,
				MaxCount:       10,
				SameAspect:     true, // a carousel is cropped to its first image otherwise
				NeedsPublicURL: true, // image_url only; there is no image upload
			},
			Caption: caption,
		}
	}
	return platform.Constraints{
		Video: &media.VideoConstraints{
			Containers:         []string{"mp4", "mov"},
			VideoCodecs:        []string{"h264", "hevc"},
			AudioCodecs:        []string{"aac"},
			MaxWidth:           1920,
			MaxHeight:          1920,
			TargetWidth:        1080,
			TargetHeight:       1920,
			AspectMin:          0.01,
			AspectMax:          10,
			FPSMin:             23,
			FPSMax:             60,
			DurationMin:        3,
			DurationMax:        15 * 60,
			MaxBytes:           300 << 20,
			MaxBitrateKbps:     25000,
			AudioSampleRateMax: 48000,
		},
		Caption: caption,
	}
}

func (a *Adapter) OptionsSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "share_to_feed": {
      "type": "boolean",
      "title": "Also show in feed (reels)",
      "description": "Show the reel in your profile grid and followers' feed, not only in the Reels tab.",
      "default": true
    }
  }
}`)
}

func (a *Adapter) DefaultOptions() json.RawMessage { return json.RawMessage(`{"share_to_feed":true}`) }

func (a *Adapter) Validate(t platform.ResolvedTarget) []platform.ValidationIssue {
	var o Options
	if err := json.Unmarshal(t.Options, &o); err != nil {
		return []platform.ValidationIssue{{Platform: ID, Severity: platform.SeverityError, Scope: platform.ScopeContent, Code: "OPTIONS_INVALID", Message: "Instagram options are not valid: " + err.Error()}}
	}
	return nil
}

func (a *Adapter) ReviewNotices(json.RawMessage) []string { return nil }

// ---- Auth ----

func (a *Adapter) AuthorizeURL(state, redirectURI, _ string) string {
	q := url.Values{
		"enable_fb_login":      {"0"},
		"force_authentication": {"1"},
		"client_id":            {a.AppID},
		"redirect_uri":         {redirectURI},
		"response_type":        {"code"},
		"scope":                {"instagram_business_basic,instagram_business_content_publish"},
		"state":                {state},
	}
	return a.AuthBase + "/oauth/authorize?" + q.Encode()
}

func (a *Adapter) RefreshMargin() time.Duration { return 7 * 24 * time.Hour }

type shortToken struct {
	AccessToken string      `json:"access_token"`
	UserID      json.Number `json:"user_id"`
	Permissions any         `json:"permissions"`
}

func (a *Adapter) Exchange(ctx context.Context, code, redirectURI, _ string) (platform.TokenSet, error) {
	form := url.Values{
		"client_id":     {a.AppID},
		"client_secret": {a.AppSecret},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
		"code":          {code},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenBase+"/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The response is either the token object or {"data":[token object]}.
	var raw struct {
		shortToken
		Data []shortToken `json:"data"`
	}
	if err := platform.DoJSON(a.HTTP, req, &raw, graphError); err != nil {
		return platform.TokenSet{}, fmt.Errorf("code exchange: %w", err)
	}
	st := raw.shortToken
	if st.AccessToken == "" && len(raw.Data) > 0 {
		st = raw.Data[0]
	}
	if st.AccessToken == "" {
		return platform.TokenSet{}, fmt.Errorf("code exchange returned no access token")
	}

	// Swap for a long-lived (~60 day) token.
	q := url.Values{"grant_type": {"ig_exchange_token"}, "client_secret": {a.AppSecret}, "access_token": {st.AccessToken}}
	ts, err := a.tokenCall(ctx, "/access_token?"+q.Encode())
	if err != nil {
		return ts, fmt.Errorf("long-lived token: %w", err)
	}

	var me struct {
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := a.graphGet(ctx, ts.AccessToken, "/me", url.Values{"fields": {"user_id,username"}}, &me); err != nil {
		return ts, fmt.Errorf("read account: %w", err)
	}
	ts.AccountID = me.UserID
	if ts.AccountID == "" {
		ts.AccountID = st.UserID.String()
	}
	ts.AccountName = me.Username
	ts.Scopes = "instagram_business_basic,instagram_business_content_publish"
	return ts, nil
}

func (a *Adapter) Refresh(ctx context.Context, t platform.TokenSet) (platform.TokenSet, error) {
	q := url.Values{"grant_type": {"ig_refresh_token"}, "access_token": {t.AccessToken}}
	ts, err := a.tokenCall(ctx, "/refresh_access_token?"+q.Encode())
	if err != nil {
		return t, err
	}
	ts.AccountID, ts.AccountName, ts.Scopes = t.AccountID, t.AccountName, t.Scopes
	return ts, nil
}

func (a *Adapter) tokenCall(ctx context.Context, pathAndQuery string) (platform.TokenSet, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.GraphBase+pathAndQuery, nil)
	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := platform.DoJSON(a.HTTP, req, &r, graphError); err != nil {
		return platform.TokenSet{}, err
	}
	return platform.TokenSet{AccessToken: r.AccessToken, ExpiresAt: time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)}, nil
}

// ---- Publish ----

func (a *Adapter) Publish(ctx context.Context, pc platform.PublishContext, t platform.ResolvedTarget) (platform.PublishResult, error) {
	var o Options
	if err := json.Unmarshal(t.Options, &o); err != nil {
		return platform.PublishResult{}, err
	}
	uid := pc.Token.AccountID
	token := pc.Token.AccessToken

	if err := a.checkQuota(ctx, pc, token, uid); err != nil {
		return platform.PublishResult{}, err
	}
	var containerID string
	var err error
	if t.Kind == platform.KindImage {
		containerID, err = a.imageContainer(ctx, pc, token, uid, t)
	} else {
		containerID, err = a.reelContainer(ctx, pc, token, uid, t, o)
	}
	if err != nil {
		return platform.PublishResult{RemoteID: containerID}, err
	}

	pc.Progress("processing on Instagram")
	if err := a.waitFinished(ctx, token, containerID, pc); err != nil {
		return platform.PublishResult{RemoteID: containerID}, err
	}

	pc.Progress("publishing")
	var published struct {
		ID string `json:"id"`
	}
	if err := a.graphPost(ctx, token, "/"+uid+"/media_publish", url.Values{"creation_id": {containerID}}, &published); err != nil {
		return platform.PublishResult{RemoteID: containerID}, fmt.Errorf("publish: %w", err)
	}

	res := platform.PublishResult{RemoteID: published.ID}
	var media struct {
		Permalink string `json:"permalink"`
	}
	if err := a.graphGet(ctx, token, "/"+published.ID, url.Values{"fields": {"permalink"}}, &media); err != nil {
		pc.Log("could not read permalink: %v", err)
	}
	res.Permalink = media.Permalink
	return res, nil
}

func (a *Adapter) checkQuota(ctx context.Context, pc platform.PublishContext, token, uid string) error {
	pc.Progress("checking quota")
	var limit struct {
		Data []struct {
			QuotaUsage int `json:"quota_usage"`
			Config     struct {
				QuotaTotal int `json:"quota_total"`
			} `json:"config"`
		} `json:"data"`
	}
	if err := a.graphGet(ctx, token, "/"+uid+"/content_publishing_limit", url.Values{"fields": {"quota_usage,config"}}, &limit); err != nil {
		pc.Log("quota check failed (continuing): %v", err)
	} else if len(limit.Data) > 0 && limit.Data[0].Config.QuotaTotal > 0 && limit.Data[0].QuotaUsage >= limit.Data[0].Config.QuotaTotal {
		return fmt.Errorf("Instagram publishing quota reached (%d/%d in the last 24 h)", limit.Data[0].QuotaUsage, limit.Data[0].Config.QuotaTotal)
	}
	return nil
}

// reelContainer creates the container of a reel and uploads the video.
func (a *Adapter) reelContainer(ctx context.Context, pc platform.PublishContext, token, uid string, t platform.ResolvedTarget, o Options) (string, error) {
	pc.Progress("creating container")
	form := url.Values{
		"media_type":    {"REELS"},
		"caption":       {t.Caption},
		"share_to_feed": {strconv.FormatBool(o.ShareToFeed)},
	}
	// Prefer letting Instagram fetch the file from a public link (works for
	// all accounts); fall back to a direct resumable upload without a tunnel.
	publicURL, hasPublic := "", false
	if pc.PublicURL != nil {
		publicURL, hasPublic = pc.PublicURL(t.FilePath)
	}
	if hasPublic {
		form.Set("video_url", publicURL)
	} else {
		form.Set("upload_type", "resumable")
	}
	// Cover: an uploaded image (needs a public link) or a frame of the video.
	coverSet := false
	if t.CoverImagePath != "" && pc.PublicURL != nil {
		if u, ok := pc.PublicURL(t.CoverImagePath); ok {
			form.Set("cover_url", u)
			coverSet = true
		}
	}
	if !coverSet && t.CoverMs > 0 {
		form.Set("thumb_offset", strconv.FormatInt(t.CoverMs, 10))
	}
	var container struct {
		ID string `json:"id"`
	}
	if err := a.graphPost(ctx, token, "/"+uid+"/media", form, &container); err != nil {
		if !hasPublic && strings.Contains(err.Error(), "video_url") {
			return "", fmt.Errorf("Instagram needs to download the video from a public link for this account. Set up the tunnel (NGROK_DOMAIN or PUBLIC_BASE_URL, see README → Tunnel) and retry: %w", err)
		}
		return "", fmt.Errorf("create container: %w", err)
	}
	pc.Log("container %s created", container.ID)

	if hasPublic {
		pc.Log("Instagram fetches the video from %s", redactPublic(publicURL))
		return container.ID, nil
	}
	pc.Progress("uploading")
	if err := a.upload(ctx, token, container.ID, t.FilePath); err != nil {
		return container.ID, fmt.Errorf("upload: %w", err)
	}
	return container.ID, nil
}

// imageContainer creates the container of an image post: one image, or a
// carousel of child containers. Instagram only takes images by public link.
func (a *Adapter) imageContainer(ctx context.Context, pc platform.PublishContext, token, uid string, t platform.ResolvedTarget) (string, error) {
	if len(t.Images) == 0 {
		return "", fmt.Errorf("the post has no converted images")
	}
	urls := make([]string, len(t.Images))
	for i, img := range t.Images {
		ok := false
		if pc.PublicURL != nil {
			urls[i], ok = pc.PublicURL(img.Path)
		}
		if !ok {
			return "", fmt.Errorf("Instagram downloads images from a public link. Set up the tunnel (NGROK_DOMAIN or PUBLIC_BASE_URL, see README → Tunnel) and retry")
		}
	}
	pc.Log("Instagram fetches %d image(s) from %s", len(urls), redactPublic(urls[0]))

	var container struct {
		ID string `json:"id"`
	}
	if len(urls) == 1 {
		pc.Progress("creating container")
		if err := a.graphPost(ctx, token, "/"+uid+"/media", url.Values{"image_url": {urls[0]}, "caption": {t.Caption}}, &container); err != nil {
			return "", fmt.Errorf("create container: %w", err)
		}
		pc.Log("container %s created", container.ID)
		return container.ID, nil
	}

	children := make([]string, len(urls))
	for i, u := range urls {
		pc.Progress(fmt.Sprintf("adding image %d/%d", i+1, len(urls)))
		var child struct {
			ID string `json:"id"`
		}
		if err := a.graphPost(ctx, token, "/"+uid+"/media", url.Values{"image_url": {u}, "is_carousel_item": {"true"}}, &child); err != nil {
			return "", fmt.Errorf("image %d: %w", i+1, err)
		}
		if err := a.waitFinished(ctx, token, child.ID, pc); err != nil {
			return "", fmt.Errorf("image %d: %w", i+1, err)
		}
		children[i] = child.ID
	}
	pc.Progress("creating carousel")
	form := url.Values{"media_type": {"CAROUSEL"}, "children": {strings.Join(children, ",")}, "caption": {t.Caption}}
	if err := a.graphPost(ctx, token, "/"+uid+"/media", form, &container); err != nil {
		return "", fmt.Errorf("create carousel: %w", err)
	}
	pc.Log("carousel %s created from %d images", container.ID, len(children))
	return container.ID, nil
}

// redactPublic shortens a signed public media link for the log.
func redactPublic(u string) string {
	return strings.SplitN(u, "/public-media/", 2)[0] + "/public-media/…"
}

func (a *Adapter) upload(ctx context.Context, token, containerID, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/ig-api-upload/%s/%s", a.UploadBase, a.Version, containerID), f)
	req.ContentLength = st.Size()
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("offset", "0")
	req.Header.Set("file_size", strconv.FormatInt(st.Size(), 10))
	var r struct {
		Success bool `json:"success"`
	}
	if err := platform.DoJSON(a.HTTP, req, &r, graphError); err != nil {
		return err
	}
	if !r.Success {
		return fmt.Errorf("upload not acknowledged")
	}
	return nil
}

func (a *Adapter) waitFinished(ctx context.Context, token, containerID string, pc platform.PublishContext) error {
	deadline := time.Now().Add(a.PollTimeout)
	for {
		var s struct {
			StatusCode string `json:"status_code"`
			Status     string `json:"status"`
		}
		if err := a.graphGet(ctx, token, "/"+containerID, url.Values{"fields": {"status_code,status"}}, &s); err != nil {
			return fmt.Errorf("container status: %w", err)
		}
		switch s.StatusCode {
		case "FINISHED":
			return nil
		case "ERROR", "EXPIRED":
			return fmt.Errorf("Instagram rejected the media (%s): %s", s.StatusCode, s.Status)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Instagram is still processing after %s (status %s)", a.PollTimeout, s.StatusCode)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.PollInterval):
		}
	}
}

func (a *Adapter) graphGet(ctx context.Context, token, path string, q url.Values, out any) error {
	q.Set("access_token", token)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s%s?%s", a.GraphBase, a.Version, path, q.Encode()), nil)
	return platform.DoJSON(a.HTTP, req, out, graphError)
}

func (a *Adapter) graphPost(ctx context.Context, token, path string, form url.Values, out any) error {
	form.Set("access_token", token)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/%s%s", a.GraphBase, a.Version, path), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return platform.DoJSON(a.HTTP, req, out, graphError)
}

func graphError(body []byte) string {
	var e struct {
		Error struct {
			Message      string `json:"message"`
			ErrorUserMsg string `json:"error_user_msg"`
			Code         int    `json:"code"`
		} `json:"error"`
		ErrorMessage string `json:"error_message"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	switch {
	case e.Error.ErrorUserMsg != "":
		return e.Error.ErrorUserMsg
	case e.Error.Code == 200 && strings.Contains(e.Error.Message, "API access blocked"):
		// Meta answers every call of this token like this; it's not about the post.
		return fmt.Sprintf("%s (code 200). Instagram blocks all API calls of this account/app right now: check the Meta app dashboard for alerts or restrictions and the Instagram app for account notices, then reconnect Instagram in Settings", e.Error.Message)
	case e.Error.Message != "":
		return fmt.Sprintf("%s (code %d)", e.Error.Message, e.Error.Code)
	default:
		return e.ErrorMessage
	}
}

// MissingSettings implements platform.SetupReporter.
func (a *Adapter) MissingSettings() []string {
	var m []string
	if a.AppID == "" {
		m = append(m, "INSTAGRAM_APP_ID")
	}
	if a.AppSecret == "" {
		m = append(m, "INSTAGRAM_APP_SECRET")
	}
	return m
}

// TokenPrompt implements platform.TokenPrompter.
func (a *Adapter) TokenPrompt() platform.TokenPrompt {
	return platform.TokenPrompt{
		Label: "Access token",
		Help:  "Meta dashboard → your app → Instagram → API setup with Instagram login → Generate access tokens → add your account → Generate. No tunnel needed.",
	}
}

// ImportToken implements platform.TokenImporter for tokens generated in the
// Meta dashboard ("Generate access tokens"). Those are long-lived (~60 days)
// and are refreshed automatically like OAuth tokens.
func (a *Adapter) ImportToken(ctx context.Context, accessToken string) (platform.TokenSet, error) {
	var me struct {
		UserID   string `json:"user_id"`
		Username string `json:"username"`
	}
	if err := a.graphGet(ctx, accessToken, "/me", url.Values{"fields": {"user_id,username"}}, &me); err != nil {
		return platform.TokenSet{}, fmt.Errorf("Instagram rejected the token: %w", err)
	}
	if me.UserID == "" {
		return platform.TokenSet{}, fmt.Errorf("token does not belong to an Instagram professional account")
	}
	return platform.TokenSet{
		AccessToken: accessToken,
		ExpiresAt:   time.Now().Add(60 * 24 * time.Hour),
		AccountID:   me.UserID,
		AccountName: me.Username,
		Scopes:      "instagram_business_basic,instagram_business_content_publish",
	}, nil
}

// IsLive implements platform.LiveChecker: Instagram answers "does not exist"
// (code 100) for media that was deleted.
func (a *Adapter) IsLive(ctx context.Context, t platform.TokenSet, remoteID string) (bool, error) {
	var m struct {
		ID string `json:"id"`
	}
	err := a.graphGet(ctx, t.AccessToken, "/"+remoteID, url.Values{"fields": {"id"}}, &m)
	if err == nil {
		return true, nil
	}
	var apiErr *platform.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 400 &&
		(strings.Contains(apiErr.Body, "does not exist") || strings.Contains(apiErr.Message, "(code 100)")) {
		return false, nil
	}
	return false, err
}

// Stats implements platform.StatsProvider. Likes and comments come with
// instagram_business_basic; views need instagram_business_manage_insights.
func (a *Adapter) Stats(ctx context.Context, t platform.TokenSet, remoteID string) (platform.Stats, error) {
	s := platform.Stats{At: time.Now()}
	var m struct {
		LikeCount     *int64 `json:"like_count"`
		CommentsCount *int64 `json:"comments_count"`
	}
	if err := a.graphGet(ctx, t.AccessToken, "/"+remoteID, url.Values{"fields": {"like_count,comments_count"}}, &m); err != nil {
		return s, err
	}
	s.Likes, s.Comments = m.LikeCount, m.CommentsCount

	var ins struct {
		Data []struct {
			Name   string `json:"name"`
			Values []struct {
				Value int64 `json:"value"`
			} `json:"values"`
			TotalValue *struct {
				Value int64 `json:"value"`
			} `json:"total_value"`
		} `json:"data"`
	}
	if err := a.graphGet(ctx, t.AccessToken, "/"+remoteID+"/insights", url.Values{"metric": {"views"}}, &ins); err != nil {
		s.Note = "Views need the instagram_business_manage_insights permission (generate a new token after adding it)."
		return s, nil
	}
	for _, d := range ins.Data {
		if d.Name != "views" {
			continue
		}
		var v int64
		switch {
		case d.TotalValue != nil:
			v = d.TotalValue.Value
		case len(d.Values) > 0:
			v = d.Values[0].Value
		}
		s.Views = &v
	}
	return s, nil
}

// EmbedURL implements platform.Embedder: https://www.instagram.com/reel/<code>/embed/
func (a *Adapter) EmbedURL(permalink, _ string) string {
	u, err := url.Parse(permalink)
	if err != nil || !strings.HasSuffix(u.Host, "instagram.com") {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || (parts[0] != "reel" && parts[0] != "p" && parts[0] != "tv") {
		return ""
	}
	return "https://www.instagram.com/" + parts[0] + "/" + parts[1] + "/embed/"
}
