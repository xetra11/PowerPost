// Package tiktok publishes videos via TikTok's Content Posting API.
package tiktok

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"powerposter/internal/media"
	"powerposter/internal/platform"
)

const ID platform.ID = "tiktok"

const (
	ModeInbox  = "inbox"  // upload to the creator's TikTok inbox; they finish posting in the app
	ModeDirect = "direct" // post directly (private-only until the app passes TikTok's audit)
)

var privacyLevels = []string{"PUBLIC_TO_EVERYONE", "MUTUAL_FOLLOW_FRIENDS", "FOLLOWER_OF_CREATOR", "SELF_ONLY"}

type Adapter struct {
	ClientKey    string
	ClientSecret string

	AuthBase string // https://www.tiktok.com
	APIBase  string // https://open.tiktokapis.com

	HTTP         *http.Client
	PollInterval time.Duration
	PollTimeout  time.Duration
}

func New(clientKey, clientSecret string) *Adapter {
	return &Adapter{
		ClientKey: clientKey, ClientSecret: clientSecret,
		AuthBase:     "https://www.tiktok.com",
		APIBase:      "https://open.tiktokapis.com",
		HTTP:         &http.Client{Timeout: 30 * time.Minute},
		PollInterval: 5 * time.Second,
		PollTimeout:  15 * time.Minute,
	}
}

type Options struct {
	Mode              string `json:"mode"`
	PrivacyLevel      string `json:"privacy_level"`
	DisableComment    bool   `json:"disable_comment"`
	DisableDuet       bool   `json:"disable_duet"`
	DisableStitch     bool   `json:"disable_stitch"`
	CommercialContent bool   `json:"commercial_content"`
	BrandOrganic      bool   `json:"brand_organic"` // "Your brand" → labelled "Promotional content"
	BrandContent      bool   `json:"brand_content"` // "Branded content" → labelled "Paid partnership"
	IsAIGC            bool   `json:"is_aigc"`
	AutoAddMusic      bool   `json:"auto_add_music"` // photo posts only
}

func (a *Adapter) ID() platform.ID     { return ID }
func (a *Adapter) DisplayName() string { return "TikTok" }
func (a *Adapter) SupportedKinds() []platform.PostKind {
	return []platform.PostKind{platform.KindVideo, platform.KindImage}
}
func (a *Adapter) Configured() bool             { return a.ClientKey != "" && a.ClientSecret != "" }
func (a *Adapter) Auth() platform.Authenticator { return a }

// Constraints follow https://developers.tiktok.com/doc/content-posting-api-media-transfer-guide.
// The API accepts up to 4096 px, but TikTok re-encodes to ≤1080p anyway, so we
// convert to 1080x1920 ourselves: smaller uploads and we control the quality.
// Photo posts take JPEG/WebP up to 1080p, pulled from a public link.
func (a *Adapter) Constraints(kind platform.PostKind) platform.Constraints {
	if kind == platform.KindImage {
		return platform.Constraints{
			Image: &media.ImageConstraints{
				Formats:        []string{"jpeg", "webp"},
				MaxWidth:       1080,
				MaxHeight:      1920,
				MaxBytes:       20 << 20,
				MaxCount:       35,
				NeedsPublicURL: true, // photos are only accepted as PULL_FROM_URL
			},
			Caption: platform.CaptionRules{MaxChars: 4000, CountUTF16: true}, // photo description
		}
	}
	return platform.Constraints{
		Video: &media.VideoConstraints{
			Containers:   []string{"mp4", "mov"},
			VideoCodecs:  []string{"h264", "hevc"}, // the API also takes VP8/9 in WebM; we always deliver MP4
			AudioCodecs:  []string{"aac", "mp3"},
			MaxWidth:     4096,
			MaxHeight:    4096,
			TargetWidth:  1080,
			TargetHeight: 1920,
			FPSMin:       23,
			FPSMax:       60,
			DurationMin:  1,
			DurationMax:  10 * 60, // the per-creator limit is checked at publish time
			MaxBytes:     4 << 30,
		},
		Caption: platform.CaptionRules{MaxChars: 2200, CountUTF16: true},
	}
}

func (a *Adapter) OptionsSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "mode": {
      "type": "string",
      "title": "Posting mode",
      "enum": ["inbox", "direct"],
      "x-enumLabels": {"inbox": "Send to TikTok inbox (finish in the app)", "direct": "Post directly"},
      "description": "Direct posts are forced to private until your TikTok app passes the audit. Inbox uploads work without the audit, but the caption must be added in the TikTok app.",
      "default": "inbox"
    },
    "privacy_level": {
      "type": "string",
      "title": "Who can view this post",
      "enum": ["PUBLIC_TO_EVERYONE", "MUTUAL_FOLLOW_FRIENDS", "FOLLOWER_OF_CREATOR", "SELF_ONLY"],
      "x-enumLabels": {"PUBLIC_TO_EVERYONE": "Everyone", "MUTUAL_FOLLOW_FRIENDS": "Friends", "FOLLOWER_OF_CREATOR": "Followers", "SELF_ONLY": "Only me"},
      "description": "Required for direct posts. TikTok requires you to choose; there is no default."
    },
    "disable_comment": {"type": "boolean", "title": "Turn off comments", "default": false},
    "disable_duet": {"type": "boolean", "title": "Turn off Duet (videos)", "default": false},
    "disable_stitch": {"type": "boolean", "title": "Turn off Stitch (videos)", "default": false},
    "auto_add_music": {"type": "boolean", "title": "Add recommended music (photo posts)", "description": "Lets TikTok pick a sound for photo posts. Has no effect on videos.", "default": true},
    "commercial_content": {
      "type": "boolean",
      "title": "This video promotes a brand, product or service",
      "description": "Required disclosure for ads. Promoting your own game counts.",
      "default": true
    },
    "brand_organic": {"type": "boolean", "title": "Your brand (labelled \"Promotional content\")", "default": true},
    "brand_content": {"type": "boolean", "title": "Branded content for a third party (labelled \"Paid partnership\")", "default": false},
    "is_aigc": {"type": "boolean", "title": "Contains AI-generated content", "default": false}
  }
}`)
}

func (a *Adapter) DefaultOptions() json.RawMessage {
	return json.RawMessage(`{"mode":"inbox","privacy_level":"","disable_comment":false,"disable_duet":false,"disable_stitch":false,"auto_add_music":true,"commercial_content":true,"brand_organic":true,"brand_content":false,"is_aigc":false}`)
}

func (a *Adapter) Validate(t platform.ResolvedTarget) []platform.ValidationIssue {
	var out []platform.ValidationIssue
	add := func(sev platform.Severity, code, msg string) {
		out = append(out, platform.ValidationIssue{Platform: ID, Severity: sev, Scope: platform.ScopeContent, Code: code, Message: msg})
	}
	var o Options
	if err := json.Unmarshal(t.Options, &o); err != nil {
		add(platform.SeverityError, "OPTIONS_INVALID", "TikTok options are not valid: "+err.Error())
		return out
	}
	switch o.Mode {
	case ModeInbox:
		if t.Kind == platform.KindImage {
			add(platform.SeverityInfo, "INBOX_CAPTION", "Inbox mode: the photos and caption go to your TikTok inbox; you finish the post in the TikTok app.")
		} else {
			add(platform.SeverityInfo, "INBOX_CAPTION", "Inbox mode: TikTok does not accept a caption through the API. Copy it from the review page when you finish the post in the TikTok app.")
		}
		return out
	case ModeDirect:
	default:
		add(platform.SeverityError, "MODE_INVALID", `Posting mode must be "inbox" or "direct".`)
		return out
	}
	if o.PrivacyLevel == "" {
		add(platform.SeverityError, "PRIVACY_REQUIRED", "Choose who can view this post. TikTok requires an explicit choice.")
	} else if !slices.Contains(privacyLevels, o.PrivacyLevel) {
		add(platform.SeverityError, "PRIVACY_INVALID", "Unknown privacy level "+o.PrivacyLevel+".")
	}
	if o.CommercialContent && !o.BrandOrganic && !o.BrandContent {
		add(platform.SeverityError, "DISCLOSURE_INCOMPLETE", `Commercial content is on: select "Your brand" and/or "Branded content".`)
	}
	if o.CommercialContent && o.BrandContent && o.PrivacyLevel == "SELF_ONLY" {
		add(platform.SeverityError, "BRANDED_PRIVATE", "Branded content can't be posted with visibility \"Only me\".")
	}
	if o.PrivacyLevel != "" && o.PrivacyLevel != "SELF_ONLY" {
		add(platform.SeverityInfo, "AUDIT_NOTE", "Until your TikTok app passes the audit, direct posts only work for private accounts and are visible to you only.")
	}
	return out
}

func (a *Adapter) ReviewNotices(raw json.RawMessage) []string {
	var o Options
	if json.Unmarshal(raw, &o) != nil {
		return nil
	}
	if o.Mode == ModeInbox {
		return []string{"The post will be sent to your TikTok inbox. Open TikTok to finish and post it there."}
	}
	var n []string
	if o.CommercialContent && o.BrandContent {
		n = append(n, "By posting, you agree to TikTok's Branded Content Policy and Music Usage Confirmation.")
		n = append(n, `Your video will be labeled "Paid partnership".`)
	} else {
		n = append(n, "By posting, you agree to TikTok's Music Usage Confirmation.")
		if o.CommercialContent && o.BrandOrganic {
			n = append(n, `Your video will be labeled "Promotional content".`)
		}
	}
	return n
}

// ---- Auth ----

// AuthorizeURL uses PKCE, which TikTok requires for desktop (localhost) redirects.
// Unlike RFC 7636, TikTok expects the challenge as a hex-encoded SHA-256.
func (a *Adapter) AuthorizeURL(state, redirectURI, verifier string) string {
	q := url.Values{
		"client_key":    {a.ClientKey},
		"response_type": {"code"},
		"scope":         {"user.info.basic,video.publish,video.upload"},
		"redirect_uri":  {redirectURI},
		"state":         {state},
	}
	if verifier != "" {
		sum := sha256.Sum256([]byte(verifier))
		q.Set("code_challenge", hex.EncodeToString(sum[:]))
		q.Set("code_challenge_method", "S256")
	}
	return a.AuthBase + "/v2/auth/authorize/?" + q.Encode()
}

func (a *Adapter) RefreshMargin() time.Duration { return time.Hour }

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	OpenID           string `json:"open_id"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (a *Adapter) Exchange(ctx context.Context, code, redirectURI, verifier string) (platform.TokenSet, error) {
	form := url.Values{
		"client_key":    {a.ClientKey},
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
	var info struct {
		User struct {
			DisplayName string `json:"display_name"`
		} `json:"user"`
	}
	if err := a.api(ctx, ts.AccessToken, http.MethodGet, "/v2/user/info/?fields=open_id,display_name", nil, &info); err != nil {
		return ts, fmt.Errorf("read account: %w", err)
	}
	ts.AccountName = info.User.DisplayName
	return ts, nil
}

func (a *Adapter) Refresh(ctx context.Context, t platform.TokenSet) (platform.TokenSet, error) {
	ts, err := a.token(ctx, url.Values{
		"client_key":    {a.ClientKey},
		"client_secret": {a.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	})
	if err != nil {
		return t, err
	}
	ts.AccountName = t.AccountName
	return ts, nil
}

func (a *Adapter) token(ctx context.Context, form url.Values) (platform.TokenSet, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.APIBase+"/v2/oauth/token/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var r tokenResponse
	if err := platform.DoJSON(a.HTTP, req, &r, oauthError); err != nil {
		return platform.TokenSet{}, err
	}
	if r.AccessToken == "" {
		return platform.TokenSet{}, fmt.Errorf("%s: %s", r.Error, r.ErrorDescription)
	}
	now := time.Now()
	return platform.TokenSet{
		AccessToken:      r.AccessToken,
		RefreshToken:     r.RefreshToken,
		ExpiresAt:        now.Add(time.Duration(r.ExpiresIn) * time.Second),
		RefreshExpiresAt: now.Add(time.Duration(r.RefreshExpiresIn) * time.Second),
		AccountID:        r.OpenID,
		Scopes:           r.Scope,
	}, nil
}

// ---- Creator info ----

type CreatorInfo struct {
	CreatorAvatarURL        string   `json:"creator_avatar_url"`
	CreatorUsername         string   `json:"creator_username"`
	CreatorNickname         string   `json:"creator_nickname"`
	PrivacyLevelOptions     []string `json:"privacy_level_options"`
	CommentDisabled         bool     `json:"comment_disabled"`
	DuetDisabled            bool     `json:"duet_disabled"`
	StitchDisabled          bool     `json:"stitch_disabled"`
	MaxVideoPostDurationSec int      `json:"max_video_post_duration_sec"`
}

func (a *Adapter) creatorInfo(ctx context.Context, token string) (CreatorInfo, error) {
	var ci CreatorInfo
	err := a.api(ctx, token, http.MethodPost, "/v2/post/publish/creator_info/query/", map[string]any{}, &ci)
	return ci, err
}

// AccountInfo implements platform.AccountInfoProvider; the UI must show the creator's nickname.
func (a *Adapter) AccountInfo(ctx context.Context, t platform.TokenSet) (any, error) {
	return a.creatorInfo(ctx, t.AccessToken)
}

// ---- Publish ----

const (
	chunkSize      = 10 << 20
	maxSingleChunk = 64 << 20
)

// chunking follows TikTok's rules: files ≤64 MB go in one chunk; otherwise
// 10 MB chunks with the remainder merged into the last one.
func chunking(size int64) (chunk int64, count int64) {
	if size <= maxSingleChunk {
		return size, 1
	}
	return chunkSize, size / chunkSize
}

type initResponse struct {
	PublishID string `json:"publish_id"`
	UploadURL string `json:"upload_url"`
}

func (a *Adapter) Publish(ctx context.Context, pc platform.PublishContext, t platform.ResolvedTarget) (platform.PublishResult, error) {
	var o Options
	if err := json.Unmarshal(t.Options, &o); err != nil {
		return platform.PublishResult{}, err
	}
	token := pc.Token.AccessToken

	pc.Progress("checking creator info")
	ci, err := a.creatorInfo(ctx, token)
	if err != nil {
		return platform.PublishResult{}, fmt.Errorf("creator info: %w", err)
	}
	pc.Log("posting as %s (@%s)", ci.CreatorNickname, ci.CreatorUsername)
	if o.Mode != ModeInbox && len(ci.PrivacyLevelOptions) > 0 && !slices.Contains(ci.PrivacyLevelOptions, o.PrivacyLevel) {
		return platform.PublishResult{}, fmt.Errorf("privacy level %s is not available for this account (allowed: %s)", o.PrivacyLevel, strings.Join(ci.PrivacyLevelOptions, ", "))
	}
	if t.Kind == platform.KindImage {
		return a.publishPhotos(ctx, pc, t, o, ci)
	}
	if ci.MaxVideoPostDurationSec > 0 && t.File.Duration > float64(ci.MaxVideoPostDurationSec) {
		return platform.PublishResult{}, fmt.Errorf("video is %s; this account may post at most %s", media.FormatDuration(t.File.Duration), media.FormatDuration(float64(ci.MaxVideoPostDurationSec)))
	}

	st, err := os.Stat(t.FilePath)
	if err != nil {
		return platform.PublishResult{}, err
	}
	chunk, count := chunking(st.Size())
	source := map[string]any{"source": "FILE_UPLOAD", "video_size": st.Size(), "chunk_size": chunk, "total_chunk_count": count}

	pc.Progress("initializing upload")
	var init initResponse
	if o.Mode == ModeInbox {
		err = a.api(ctx, token, http.MethodPost, "/v2/post/publish/inbox/video/init/", map[string]any{"source_info": source}, &init)
	} else {
		postInfo := map[string]any{
			"title":                    t.Caption,
			"privacy_level":            o.PrivacyLevel,
			"disable_comment":          o.DisableComment || ci.CommentDisabled,
			"disable_duet":             o.DisableDuet || ci.DuetDisabled,
			"disable_stitch":           o.DisableStitch || ci.StitchDisabled,
			"video_cover_timestamp_ms": t.CoverMs,
			"brand_content_toggle":     o.CommercialContent && o.BrandContent,
			"brand_organic_toggle":     o.CommercialContent && o.BrandOrganic,
			"is_aigc":                  o.IsAIGC,
		}
		err = a.api(ctx, token, http.MethodPost, "/v2/post/publish/video/init/", map[string]any{"post_info": postInfo, "source_info": source}, &init)
	}
	if err != nil {
		return platform.PublishResult{}, fmt.Errorf("init upload: %w", err)
	}
	pc.Log("publish_id %s, %d bytes in %d chunk(s)", init.PublishID, st.Size(), count)

	if err := a.uploadChunks(ctx, init.UploadURL, t.FilePath, st.Size(), chunk, count, pc); err != nil {
		return platform.PublishResult{RemoteID: init.PublishID}, fmt.Errorf("upload: %w", err)
	}

	pc.Progress("processing on TikTok")
	return a.waitDone(ctx, pc, token, init.PublishID, o.Mode, ci.CreatorUsername, "video")
}

// urlPrefix is the scheme and host of a link with a trailing slash, the
// prefix to verify for PULL_FROM_URL.
func urlPrefix(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	return u.Scheme + "://" + u.Host + "/"
}

// publishPhotos posts images as a TikTok photo post. TikTok only pulls photos
// from public links on a domain verified in the developer portal.
func (a *Adapter) publishPhotos(ctx context.Context, pc platform.PublishContext, t platform.ResolvedTarget, o Options, ci CreatorInfo) (platform.PublishResult, error) {
	if len(t.Images) == 0 {
		return platform.PublishResult{}, fmt.Errorf("the post has no converted images")
	}
	urls := make([]string, len(t.Images))
	for i, img := range t.Images {
		ok := false
		if pc.PublicURL != nil {
			urls[i], ok = pc.PublicURL(img.Path)
		}
		if !ok {
			return platform.PublishResult{}, fmt.Errorf("TikTok downloads photos from a public link. Set up the tunnel (NGROK_DOMAIN or PUBLIC_BASE_URL, see README → Tunnel) and retry")
		}
	}

	postInfo := map[string]any{"description": t.Caption}
	mode := "MEDIA_UPLOAD"
	if o.Mode == ModeDirect {
		mode = "DIRECT_POST"
		postInfo["privacy_level"] = o.PrivacyLevel
		postInfo["disable_comment"] = o.DisableComment || ci.CommentDisabled
		postInfo["auto_add_music"] = o.AutoAddMusic
		postInfo["brand_content_toggle"] = o.CommercialContent && o.BrandContent
		postInfo["brand_organic_toggle"] = o.CommercialContent && o.BrandOrganic
	}
	body := map[string]any{
		"post_info":   postInfo,
		"source_info": map[string]any{"source": "PULL_FROM_URL", "photo_cover_index": 0, "photo_images": urls},
		"post_mode":   mode,
		"media_type":  "PHOTO",
	}

	pc.Progress("sending photos")
	var init initResponse
	if err := a.api(ctx, pc.Token.AccessToken, http.MethodPost, "/v2/post/publish/content/init/", body, &init); err != nil {
		if strings.Contains(err.Error(), "url_ownership_unverified") {
			return platform.PublishResult{}, fmt.Errorf("TikTok only pulls photos from a verified URL: in the TikTok developer portal, verify the URL prefix %s (Verify properties → URL prefix) and put the signature file it gives you into the inbox's \"verify\" folder (see README → TikTok → Photo posts): %w", urlPrefix(urls[0]), err)
		}
		return platform.PublishResult{}, fmt.Errorf("init photo post: %w", err)
	}
	pc.Log("publish_id %s, TikTok fetches %d photo(s)", init.PublishID, len(urls))

	pc.Progress("processing on TikTok")
	return a.waitDone(ctx, pc, pc.Token.AccessToken, init.PublishID, o.Mode, ci.CreatorUsername, "photo")
}

func (a *Adapter) uploadChunks(ctx context.Context, uploadURL, path string, size, chunk, count int64, pc platform.PublishContext) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for i := int64(0); i < count; i++ {
		start := i * chunk
		end := start + chunk - 1
		if i == count-1 {
			end = size - 1 // last chunk takes the remainder
		}
		pc.Progress(fmt.Sprintf("uploading %d/%d", i+1, count))
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, io.NewSectionReader(f, start, end-start+1))
		req.ContentLength = end - start + 1
		req.Header.Set("Content-Type", "video/mp4")
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		if err := platform.DoJSON(a.HTTP, req, nil, nil); err != nil {
			return fmt.Errorf("chunk %d/%d: %w", i+1, count, err)
		}
	}
	return nil
}

// waitDone polls until TikTok finished the post. kind is the permalink path: video or photo.
func (a *Adapter) waitDone(ctx context.Context, pc platform.PublishContext, token, publishID, mode, username, kind string) (platform.PublishResult, error) {
	res := platform.PublishResult{RemoteID: publishID}
	deadline := time.Now().Add(a.PollTimeout)
	last := ""
	for {
		var s struct {
			Status     string            `json:"status"`
			FailReason string            `json:"fail_reason"`
			PostIDs    []json.RawMessage `json:"publicaly_available_post_id"` // sic (TikTok spelling); numbers or strings
			Uploaded   int64             `json:"uploaded_bytes"`
			Downloaded int64             `json:"downloaded_bytes"`
		}
		if err := a.api(ctx, token, http.MethodPost, "/v2/post/publish/status/fetch/", map[string]any{"publish_id": publishID}, &s); err != nil {
			return res, fmt.Errorf("status: %w", err)
		}
		// Shown live on the review page; the attempt log is only saved at the end.
		if cur := fmt.Sprintf("%s, %d bytes uploaded, %d downloaded", s.Status, s.Uploaded, s.Downloaded); cur != last {
			last = cur
			pc.Progress(fmt.Sprintf("processing on TikTok (%s)", cur))
		}
		switch s.Status {
		case "SEND_TO_USER_INBOX":
			if mode == ModeInbox {
				res.Note = "Sent to your TikTok inbox. Open TikTok to add the caption and post it."
				return res, nil
			}
		case "PUBLISH_COMPLETE":
			if len(s.PostIDs) > 0 {
				id := strings.Trim(string(s.PostIDs[0]), `"`) // keep big numeric IDs exact
				res.RemoteID = id
				if username != "" {
					res.Permalink = fmt.Sprintf("https://www.tiktok.com/@%s/%s/%s", username, kind, id)
				}
			} else {
				res.Note = "Posted. TikTok returned no public link (private post or still in moderation)."
			}
			return res, nil
		case "FAILED":
			return res, fmt.Errorf("TikTok rejected the post: %s", s.FailReason)
		}
		if time.Now().After(deadline) {
			return res, fmt.Errorf("TikTok is still processing after %s (status %s)", a.PollTimeout, s.Status)
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(a.PollInterval):
		}
	}
}

// api calls an Open API endpoint that uses the {"data":…, "error":{code,message}} envelope.
func (a *Adapter) api(ctx context.Context, token, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(ctx, method, a.APIBase+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error apiError        `json:"error"`
	}
	if err := platform.DoJSON(a.HTTP, req, &env, envelopeError); err != nil {
		return err
	}
	if env.Error.Code != "" && env.Error.Code != "ok" {
		return fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	LogID   string `json:"log_id"`
}

func envelopeError(body []byte) string {
	var env struct {
		Error apiError `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || env.Error.Code == "" {
		return oauthError(body)
	}
	return fmt.Sprintf("%s: %s", env.Error.Code, env.Error.Message)
}

func oauthError(body []byte) string {
	var r tokenResponse
	if json.Unmarshal(body, &r) != nil || r.Error == "" {
		return ""
	}
	return r.Error + ": " + r.ErrorDescription
}

// MissingSettings implements platform.SetupReporter.
func (a *Adapter) MissingSettings() []string {
	var m []string
	if a.ClientKey == "" {
		m = append(m, "TIKTOK_CLIENT_KEY")
	}
	if a.ClientSecret == "" {
		m = append(m, "TIKTOK_CLIENT_SECRET")
	}
	return m
}

// AllowsLocalRedirect implements platform.LocalRedirector: TikTok accepts
// http://localhost redirect URIs for apps registered with the Desktop platform.
func (a *Adapter) AllowsLocalRedirect() bool { return true }

// EmbedURL implements platform.Embedder for public posts (inbox uploads and
// private posts have no public video ID).
func (a *Adapter) EmbedURL(permalink, remoteID string) string {
	if !strings.Contains(permalink, "/video/") || remoteID == "" {
		return ""
	}
	for _, c := range remoteID {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return "https://www.tiktok.com/embed/v2/" + remoteID
}
