package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"powerposter/internal/media"
	"powerposter/internal/platform"
	"powerposter/internal/store"
)

const (
	StatusDraft      = "draft"
	StatusApproved   = "approved"
	StatusPublishing = "publishing"
	StatusPublished  = "published"
	StatusPartial    = "partial"
	StatusFailed     = "failed"
)

type AssetView struct {
	store.Asset
	URL      string `json:"url"`
	ThumbURL string `json:"thumbUrl"`
}

type RenditionView struct {
	store.Rendition
	URL string `json:"url"`
}

type TargetView struct {
	store.Target
	DisplayName   string                     `json:"displayName"`
	FinalCaption  string                     `json:"finalCaption"`
	CaptionLength int                        `json:"captionLength"`
	CaptionMax    int                        `json:"captionMax"`
	Rendition     *RenditionView             `json:"rendition"`  // the video, or the first image
	Renditions    []RenditionView            `json:"renditions"` // one per post media, in order
	Issues        []platform.ValidationIssue `json:"issues"`
	Notices       []string                   `json:"notices"`
	CanPublish    bool                       `json:"canPublish"` // this platform can be published now
	EmbedURL      string                     `json:"embedUrl"`   // iframe URL of the live post, if embeddable
}

type PostView struct {
	store.Post
	Project       ProjectRef                 `json:"project"`
	Asset         *AssetView                 `json:"asset"`         // the video, or the first image
	Assets        []AssetView                `json:"assets"`        // all media in order (images of a carousel)
	CoverURL      string                     `json:"coverUrl"`      // cover preview (video frame or custom image)
	CoverFrameURL string                     `json:"coverFrameUrl"` // video frame at coverMs
	Targets       []TargetView               `json:"targets"`
	Issues        []platform.ValidationIssue `json:"issues"`
	Ready         bool                       `json:"ready"`         // can be approved
	ApprovalValid bool                       `json:"approvalValid"` // approved and unchanged since
	CanPublish    bool                       `json:"canPublish"`    // at least one platform can be published now
	CanPublishAll bool                       `json:"canPublishAll"` // every remaining platform can be published now
	ContentHash   string                     `json:"-"`
	EditURL       string                     `json:"editUrl"`
	ReviewURL     string                     `json:"reviewUrl"`
}

func (a *App) buildView(ctx context.Context, p store.Post) (PostView, error) {
	v := PostView{
		Post:      p,
		Assets:    []AssetView{},
		Targets:   []TargetView{},
		Issues:    []platform.ValidationIssue{},
		EditURL:   fmt.Sprintf("%s/posts/%d", a.Cfg.BaseURL, p.ID),
		ReviewURL: fmt.Sprintf("%s/posts/%d/review", a.Cfg.BaseURL, p.ID),
	}
	proj, err := a.Store.GetProject(ctx, p.ProjectID)
	if err != nil {
		return v, fmt.Errorf("project of post %d: %w", p.ID, err)
	}
	v.Project = ProjectRef{ID: proj.ID, Slug: proj.Slug, Name: proj.Name}
	assets, err := a.mediaAssets(ctx, p)
	if err != nil {
		return v, err
	}
	rends := map[int64][]store.Rendition{}
	for _, as := range assets {
		v.Assets = append(v.Assets, AssetView{Asset: as, URL: mediaURL("originals", as.Path), ThumbURL: mediaURL("thumbs", as.ThumbPath)})
		if rends[as.ID], err = a.Store.ListRenditions(ctx, as.ID); err != nil {
			return v, err
		}
	}
	if len(v.Assets) > 0 {
		v.Asset = &v.Assets[0]
	}
	targets, err := a.Store.ListTargets(ctx, p.ID)
	if err != nil {
		return v, err
	}

	allReady := len(assets) > 0
	anyEnabled := false
	for _, t := range targets {
		ad, ok := a.Registry.Get(platform.ID(t.Platform))
		if !ok {
			continue // platform removed from the registry
		}
		tv := TargetView{Target: t, DisplayName: ad.DisplayName(), Renditions: []RenditionView{}, Issues: []platform.ValidationIssue{}, Notices: []string{}}
		cons := ad.Constraints(platform.PostKind(p.Kind))
		tv.FinalCaption = a.finalCaption(p, t)
		tv.CaptionLength = cons.Caption.Length(tv.FinalCaption)
		tv.CaptionMax = cons.Caption.MaxChars
		for _, as := range assets {
			for _, r := range rends[as.ID] {
				if r.Platform == t.Platform {
					rv := RenditionView{Rendition: r}
					if r.Status == "ready" {
						rv.URL = mediaURL("renditions", r.Path)
					}
					tv.Renditions = append(tv.Renditions, rv)
				}
			}
		}
		if len(tv.Renditions) > 0 {
			tv.Rendition = &tv.Renditions[0]
		}
		if e, ok := ad.(platform.Embedder); ok && t.Status == "published" && t.Permalink != "" {
			tv.EmbedURL = e.EmbedURL(t.Permalink, t.RemoteID)
		}
		if t.Enabled {
			anyEnabled = true
			tv.Issues = a.validateTarget(ctx, ad, p, v.Assets, tv)
			if n := ad.ReviewNotices(t.Options); n != nil {
				tv.Notices = n
			}
			if !tv.converted(len(assets)) {
				allReady = false
			}
			v.Issues = append(v.Issues, tv.Issues...)
		}
		v.Targets = append(v.Targets, tv)
	}
	if !anyEnabled {
		v.Issues = append(v.Issues, platform.ValidationIssue{Severity: platform.SeverityError, Scope: platform.ScopeContent, Code: "NO_PLATFORM", Message: "Select at least one platform."})
	}

	v.ContentHash = contentHash(p, v.Targets)
	v.CoverURL = coverURL(p, v.Targets)
	v.CoverFrameURL = coverFrameURL(p, v.Targets)
	contentErrors := slices.ContainsFunc(v.Issues, func(i platform.ValidationIssue) bool {
		return i.Severity == platform.SeverityError && i.Scope == platform.ScopeContent
	})
	v.Ready = anyEnabled && allReady && !contentErrors
	v.ApprovalValid = p.ApprovedHash != "" && p.ApprovedHash == v.ContentHash

	// Platforms are published independently: one can go out while another
	// still waits for its account connection or is being uploaded.
	postOK := v.ApprovalValid && v.Ready &&
		(p.Status == StatusApproved || p.Status == StatusPartial || p.Status == StatusFailed || p.Status == StatusPublishing)
	remaining := 0
	v.CanPublishAll = postOK
	for i := range v.Targets {
		t := &v.Targets[i]
		if !t.Enabled || t.Status == "published" || t.Status == "queued" || t.Status == "publishing" {
			continue
		}
		remaining++
		t.CanPublish = postOK && !slices.ContainsFunc(t.Issues, func(i platform.ValidationIssue) bool { return i.Severity == platform.SeverityError })
		v.CanPublish = v.CanPublish || t.CanPublish
		v.CanPublishAll = v.CanPublishAll && t.CanPublish
	}
	v.CanPublishAll = v.CanPublishAll && remaining > 0
	return v, nil
}

// converted reports whether every media file of the post is converted for this platform.
func (t TargetView) converted(media int) bool {
	if media == 0 || len(t.Renditions) < media {
		return false
	}
	for _, r := range t.Renditions {
		if r.Status != "ready" {
			return false
		}
	}
	return true
}

// mediaAssets returns the post's media: its video, or its images in order.
func (a *App) mediaAssets(ctx context.Context, p store.Post) ([]store.Asset, error) {
	assets, err := a.Store.ListAssets(ctx, p.ID)
	if err == nil && p.Kind != string(platform.KindImage) && len(assets) > 1 {
		assets = assets[:1]
	}
	return assets, err
}

func (a *App) finalCaption(p store.Post, t store.Target) string {
	text, tags := p.Caption, p.Hashtags
	if t.CaptionOverride != nil {
		text = *t.CaptionOverride
	}
	if t.HashtagsOverride != nil {
		tags = *t.HashtagsOverride
	}
	return platform.ComposeCaption(text, tags)
}

func (a *App) validateTarget(ctx context.Context, ad platform.Adapter, p store.Post, assets []AssetView, tv TargetView) []platform.ValidationIssue {
	id := ad.ID()
	issues := []platform.ValidationIssue{}
	add := func(sev platform.Severity, scope platform.Scope, code, msg, fix string) {
		issues = append(issues, platform.ValidationIssue{Platform: id, Severity: sev, Scope: scope, Code: code, Message: msg, AutoFix: fix})
	}

	// Account (a pasted token works without app credentials)
	if conn, err := a.Store.GetConnection(ctx, p.ProjectID, string(id)); errors.Is(err, store.ErrNotFound) {
		if ad.Configured() || isImporter(ad) {
			add(platform.SeverityError, platform.ScopeAccount, "NOT_CONNECTED", fmt.Sprintf("Connect your %s account in Settings.", ad.DisplayName()), "")
		} else {
			add(platform.SeverityError, platform.ScopeAccount, "NOT_CONFIGURED", fmt.Sprintf("%s app credentials are missing in .env — see README → Connecting platforms.", ad.DisplayName()), "")
		}
	} else if err == nil && conn.RefreshExpiresAt != nil && time.Now().After(*conn.RefreshExpiresAt) {
		add(platform.SeverityError, platform.ScopeAccount, "CONNECTION_EXPIRED", fmt.Sprintf("Your %s connection expired. Reconnect it in Settings.", ad.DisplayName()), "")
	} else if err == nil && conn.RefreshExpiresAt == nil && len(conn.RefreshTokenEnc) == 0 && conn.ExpiresAt != nil && time.Now().After(*conn.ExpiresAt) {
		// Without a refresh token the access token's expiry ends the connection.
		// A refresh token without an expiry (Google) is renewed at publish time.
		add(platform.SeverityError, platform.ScopeAccount, "CONNECTION_EXPIRED", fmt.Sprintf("Your %s connection expired. Reconnect it in Settings.", ad.DisplayName()), "")
	}

	if !slices.Contains(ad.SupportedKinds(), platform.PostKind(p.Kind)) {
		add(platform.SeverityError, platform.ScopeContent, "KIND_UNSUPPORTED", fmt.Sprintf("%s doesn't support %s posts.", ad.DisplayName(), p.Kind), "")
		return issues
	}
	cons := ad.Constraints(platform.PostKind(p.Kind))

	// Media
	switch {
	case len(assets) == 0:
		add(platform.SeverityError, platform.ScopeContent, "NO_MEDIA", "Add a video or images.", "")
	case p.Kind == string(platform.KindImage) && cons.Image != nil:
		a.validateImages(add, ad, *cons.Image, assets, tv)
	case cons.Video != nil:
		asset := assets[0]
		for _, v := range cons.Video.DurationViolations(asset.Info) {
			add(platform.SeverityError, platform.ScopeContent, v.Code, v.Message, "")
		}
		r := tv.Rendition
		switch {
		case r == nil || r.Status == "pending":
			add(platform.SeverityInfo, platform.ScopeContent, "CONVERTING", "Waiting for conversion…", "")
		case r.Status == "processing":
			add(platform.SeverityInfo, platform.ScopeContent, "CONVERTING", fmt.Sprintf("Converting… %d%%", int(r.Progress*100)), "")
		case r.Status == "failed":
			add(platform.SeverityError, platform.ScopeContent, "CONVERSION_FAILED", "Conversion failed: "+r.Error, "")
		case r.Info != nil:
			for _, v := range cons.Video.Check(*r.Info) {
				if v.Code == "DURATION_TOO_LONG" || v.Code == "DURATION_TOO_SHORT" {
					continue // already reported for the source
				}
				add(platform.SeverityError, platform.ScopeContent, v.Code, v.Message, "")
			}
			for _, fix := range r.Plan.Fixes {
				add(platform.SeverityWarning, platform.ScopeContent, "AUTO_FIXED", "Adjusted automatically", fix)
			}
		}
		if p.CoverCustom && id == "tiktok" {
			add(platform.SeverityInfo, platform.ScopeContent, "COVER_FRAME_ONLY", fmt.Sprintf("TikTok can't use a custom cover image via the API; it uses the video frame at %.1f s.", float64(p.CoverMs)/1000), "")
		}
		if (p.CoverCustom || p.CoverMs > 0) && id == "youtube" {
			add(platform.SeverityInfo, platform.ScopeContent, "COVER_NOT_USED", "YouTube picks the Shorts thumbnail itself; the cover can't be set through the API. Change it in the YouTube app if needed.", "")
		}
		if p.CoverCustom && id == "instagram" && a.Cfg.PublicBaseURL == "" {
			add(platform.SeverityWarning, platform.ScopeContent, "COVER_NEEDS_TUNNEL", fmt.Sprintf("Custom cover images need the tunnel (NGROK_DOMAIN or PUBLIC_BASE_URL); without it Instagram uses the frame at %.1f s.", float64(p.CoverMs)/1000), "")
		}
		if p.CoverMs > 0 && float64(p.CoverMs)/1000 > asset.Info.Duration {
			add(platform.SeverityWarning, platform.ScopeContent, "COVER_OUT_OF_RANGE", "Cover time is after the end of the video; the first frame will be used.", "")
		}
	}

	issues = append(issues, cons.Caption.Check(id, tv.FinalCaption)...)
	issues = append(issues, ad.Validate(a.resolve(p, tv))...)
	return issues
}

type addIssue func(sev platform.Severity, scope platform.Scope, code, msg, fix string)

// validateImages checks the images of an image post for one platform.
func (a *App) validateImages(add addIssue, ad platform.Adapter, c media.ImageConstraints, assets []AssetView, tv TargetView) {
	if c.MaxCount > 0 && len(assets) > c.MaxCount {
		add(platform.SeverityError, platform.ScopeContent, "TOO_MANY_IMAGES", fmt.Sprintf("%s takes at most %d images per post; this one has %d.", ad.DisplayName(), c.MaxCount, len(assets)), "")
	}
	if c.NeedsPublicURL && a.Cfg.PublicBaseURL == "" {
		add(platform.SeverityError, platform.ScopeAccount, "NEEDS_TUNNEL", fmt.Sprintf("%s downloads images from a public link: set up the tunnel (NGROK_DOMAIN or PUBLIC_BASE_URL, see README → Tunnel) to publish image posts.", ad.DisplayName()), "")
	}
	converting := 0
	for i, as := range assets {
		label := ""
		if len(assets) > 1 {
			label = fmt.Sprintf("Image %d: ", i+1)
		}
		ri := slices.IndexFunc(tv.Renditions, func(r RenditionView) bool { return r.AssetID == as.ID })
		switch {
		case ri < 0 || tv.Renditions[ri].Status == "pending" || tv.Renditions[ri].Status == "processing":
			converting++
		case tv.Renditions[ri].Status == "failed":
			add(platform.SeverityError, platform.ScopeContent, "CONVERSION_FAILED", label+"Conversion failed: "+tv.Renditions[ri].Error, "")
		case tv.Renditions[ri].Info != nil:
			r := tv.Renditions[ri]
			for _, v := range c.Check(*r.Info) {
				add(platform.SeverityError, platform.ScopeContent, v.Code, label+v.Message, "")
			}
			for _, fix := range r.Plan.Fixes {
				add(platform.SeverityWarning, platform.ScopeContent, "AUTO_FIXED", "Adjusted automatically", label+fix)
			}
		}
	}
	if converting > 0 {
		add(platform.SeverityInfo, platform.ScopeContent, "CONVERTING", fmt.Sprintf("Converting… %d of %d images done", len(assets)-converting, len(assets)), "")
	}
}

func (a *App) resolve(p store.Post, tv TargetView) platform.ResolvedTarget {
	rt := platform.ResolvedTarget{PostID: p.ID, Kind: platform.PostKind(p.Kind), Caption: tv.FinalCaption, Options: tv.Options, CoverMs: p.CoverMs}
	if p.CoverCustom {
		rt.CoverImagePath = p.CoverPath
	}
	if rt.Kind == platform.KindImage {
		for _, r := range tv.Renditions {
			if r.Status == "ready" && r.Info != nil {
				rt.Images = append(rt.Images, platform.File{Path: r.Path, Info: *r.Info})
			}
		}
	} else if tv.Rendition != nil {
		rt.FilePath = tv.Rendition.Path
		if tv.Rendition.Info != nil {
			rt.File = *tv.Rendition.Info
		}
	}
	return rt
}

// contentHash covers everything that ends up on a platform. Approval is bound
// to it: any change after approval sends the post back to draft.
func contentHash(p store.Post, targets []TargetView) string {
	type part struct {
		Platform string          `json:"platform"`
		Caption  string          `json:"caption"`
		Options  json.RawMessage `json:"options"`
		File     string          `json:"file"`
	}
	parts := []part{}
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		var files []string
		for _, r := range t.Renditions {
			if r.Status == "ready" {
				files = append(files, r.Checksum)
			}
		}
		file := strings.Join(files, ",") // a single video hashes as before
		parts = append(parts, part{t.Platform, t.FinalCaption, canonicalJSON(t.Options), file})
	}
	slices.SortFunc(parts, func(x, y part) int {
		if x.Platform < y.Platform {
			return -1
		}
		return 1
	})
	cover := ""
	if p.CoverCustom {
		cover = filepath.Base(p.CoverPath) // new upload = new file name
	}
	b, _ := json.Marshal(struct {
		Kind    string `json:"kind"`
		CoverMs int64  `json:"coverMs"`
		Cover   string `json:"cover"`
		Parts   []part `json:"parts"`
	}{p.Kind, p.CoverMs, cover, parts})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// canonicalJSON re-encodes JSON with sorted object keys.
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.Marshal(v) // maps marshal with sorted keys
	return b
}
