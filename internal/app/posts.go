package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"powerposter/internal/media"
	"powerposter/internal/platform"
	"powerposter/internal/store"
)

type CreatePostInput struct {
	Title          string    `json:"title"`
	Caption        string    `json:"caption"`
	Hashtags       *[]string `json:"hashtags"`  // nil: brand default hashtags
	Platforms      []string  `json:"platforms"` // empty: default platforms from settings
	AspectStrategy string    `json:"aspectStrategy"`
	CreatedBy      string    `json:"-"`
}

func (a *App) CreatePost(ctx context.Context, in CreatePostInput) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	settings, err := a.GetSettings(ctx)
	if err != nil {
		return PostView{}, err
	}
	p := store.Post{
		Title:          strings.TrimSpace(in.Title),
		Kind:           string(platform.KindVideo),
		Caption:        in.Caption,
		AspectStrategy: in.AspectStrategy,
		Status:         StatusDraft,
		CreatedBy:      in.CreatedBy,
	}
	if p.Title == "" {
		p.Title = "Untitled post"
	}
	if p.CreatedBy == "" {
		p.CreatedBy = "ui"
	}
	if p.AspectStrategy == "" {
		p.AspectStrategy = settings.DefaultAspect
	}
	if !media.Strategy(p.AspectStrategy).Valid() {
		return PostView{}, invalid("aspectStrategy must be blur, crop or pad")
	}
	if in.Hashtags != nil {
		p.Hashtags = platform.NormalizeHashtags(*in.Hashtags)
	} else {
		p.Hashtags = platform.NormalizeHashtags(settings.Brand.DefaultHashtags)
	}
	enabled := in.Platforms
	if len(enabled) == 0 {
		enabled = settings.DefaultPlatforms
	}
	for _, id := range enabled {
		if _, ok := a.Registry.Get(platform.ID(id)); !ok {
			return PostView{}, invalid("unknown platform %q (available: %s)", id, strings.Join(a.platformIDs(), ", "))
		}
	}

	if err := a.Store.CreatePost(ctx, &p); err != nil {
		return PostView{}, err
	}
	// Every platform gets a target row so it can be switched on later.
	for _, ad := range a.Registry.All() {
		t := store.Target{PostID: p.ID, Platform: string(ad.ID()), Enabled: slices.Contains(enabled, string(ad.ID())), Options: ad.DefaultOptions()}
		if err := a.Store.UpsertTarget(ctx, &t); err != nil {
			return PostView{}, err
		}
	}
	return a.GetPost(ctx, p.ID)
}

type PostPatch struct {
	Title          *string   `json:"title"`
	Caption        *string   `json:"caption"`
	Hashtags       *[]string `json:"hashtags"`
	AspectStrategy *string   `json:"aspectStrategy"`
	CoverMs        *int64    `json:"coverMs"`
}

func (a *App) UpdatePost(ctx context.Context, id int64, patch PostPatch) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	p, err := a.editablePost(ctx, id)
	if err != nil {
		return PostView{}, err
	}
	reframe := false
	if patch.Title != nil {
		p.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.Caption != nil {
		p.Caption = *patch.Caption
	}
	if patch.Hashtags != nil {
		p.Hashtags = platform.NormalizeHashtags(*patch.Hashtags)
	}
	if patch.AspectStrategy != nil && *patch.AspectStrategy != p.AspectStrategy {
		if !media.Strategy(*patch.AspectStrategy).Valid() {
			return PostView{}, invalid("aspectStrategy must be blur, crop or pad")
		}
		p.AspectStrategy = *patch.AspectStrategy
		reframe = true
	}
	if patch.CoverMs != nil {
		if *patch.CoverMs < 0 {
			return PostView{}, invalid("coverMs must not be negative")
		}
		p.CoverMs = *patch.CoverMs
	}
	if err := a.Store.UpdatePost(ctx, &p); err != nil {
		return PostView{}, err
	}
	if reframe {
		if err := a.ensureRenditions(ctx, p.ID); err != nil {
			return PostView{}, err
		}
	}
	return a.afterEdit(ctx, p.ID)
}

type TargetPatch struct {
	Enabled          *bool           `json:"enabled"`
	CaptionOverride  *string         `json:"captionOverride"`  // set a platform-specific caption
	UseBaseCaption   bool            `json:"useBaseCaption"`   // drop the override
	HashtagsOverride *[]string       `json:"hashtagsOverride"` // set platform-specific hashtags
	UseBaseHashtags  bool            `json:"useBaseHashtags"`  // drop the override
	Options          json.RawMessage `json:"options"`          // merged into existing options
}

func (a *App) UpdateTarget(ctx context.Context, postID int64, platformID string, patch TargetPatch) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	if _, err := a.editablePost(ctx, postID); err != nil {
		return PostView{}, err
	}
	ad, ok := a.Registry.Get(platform.ID(platformID))
	if !ok {
		return PostView{}, invalid("unknown platform %q (available: %s)", platformID, strings.Join(a.platformIDs(), ", "))
	}
	targets, err := a.Store.ListTargets(ctx, postID)
	if err != nil {
		return PostView{}, err
	}
	t := store.Target{PostID: postID, Platform: platformID, Options: ad.DefaultOptions()}
	if i := slices.IndexFunc(targets, func(t store.Target) bool { return t.Platform == platformID }); i >= 0 {
		t = targets[i]
	}

	enabledChanged := false
	if patch.Enabled != nil && *patch.Enabled != t.Enabled {
		t.Enabled = *patch.Enabled
		enabledChanged = true
	}
	if patch.UseBaseCaption {
		t.CaptionOverride = nil
	} else if patch.CaptionOverride != nil {
		c := *patch.CaptionOverride
		t.CaptionOverride = &c
	}
	if patch.UseBaseHashtags {
		t.HashtagsOverride = nil
	} else if patch.HashtagsOverride != nil {
		h := platform.NormalizeHashtags(*patch.HashtagsOverride)
		t.HashtagsOverride = &h
	}
	if len(patch.Options) > 0 && string(patch.Options) != "null" {
		merged, err := mergeOptions(ad.OptionsSchema(), t.Options, patch.Options)
		if err != nil {
			return PostView{}, invalid("%s options: %v", ad.DisplayName(), err)
		}
		t.Options = merged
	}
	if err := a.Store.UpsertTarget(ctx, &t); err != nil {
		return PostView{}, err
	}
	if enabledChanged && t.Enabled {
		if err := a.ensureRenditions(ctx, postID); err != nil {
			return PostView{}, err
		}
	}
	return a.afterEdit(ctx, postID)
}

// AttachFile copies (or moves) a local video into the post, replacing the
// previous one, and starts conversion for every enabled platform.
func (a *App) AttachFile(ctx context.Context, postID int64, src, originalName string, move bool) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	if _, err := a.editablePost(ctx, postID); err != nil {
		if move {
			os.Remove(src)
		}
		return PostView{}, err
	}
	if originalName == "" {
		originalName = filepath.Base(src)
	}
	ext := strings.ToLower(filepath.Ext(originalName))
	if ext == "" || len(ext) > 6 {
		ext = ".bin"
	}
	dst := filepath.Join(a.originalsDir(), fmt.Sprintf("%d-%s%s", postID, randomHex(6), ext))
	if move {
		if err := os.Rename(src, dst); err != nil {
			return PostView{}, err
		}
	} else if err := copyFile(src, dst); err != nil {
		return PostView{}, fmt.Errorf("copy %s: %w", originalName, err)
	}

	info, err := a.Media.Probe(ctx, dst)
	if err != nil {
		os.Remove(dst)
		return PostView{}, invalid("%s is not a usable video: %v", originalName, err)
	}

	old, err := a.Store.ListAssets(ctx, postID)
	if err != nil {
		return PostView{}, err
	}
	asset := store.Asset{PostID: postID, Path: dst, OriginalName: originalName, Info: info}
	thumb := strings.TrimSuffix(filepath.Base(dst), ext) + ".jpg"
	asset.ThumbPath = filepath.Join(a.thumbsDir(), thumb)
	if err := a.Media.Thumbnail(ctx, dst, asset.ThumbPath, min(1, info.Duration/3)); err != nil {
		a.Log.Warn("thumbnail", "err", err)
		asset.ThumbPath = ""
	}
	if err := a.Store.CreateAsset(ctx, &asset); err != nil {
		return PostView{}, err
	}
	for _, o := range old {
		if err := a.deleteAsset(ctx, o); err != nil {
			a.Log.Warn("remove previous asset", "asset", o.ID, "err", err)
		}
	}

	p, err := a.Store.GetPost(ctx, postID)
	if err != nil {
		return PostView{}, err
	}
	if float64(p.CoverMs)/1000 > info.Duration {
		p.CoverMs = 0
		if err := a.Store.UpdatePost(ctx, &p); err != nil {
			return PostView{}, err
		}
	}
	if err := a.ensureRenditions(ctx, postID); err != nil {
		return PostView{}, err
	}
	return a.afterEdit(ctx, postID)
}

// AttachUpload stores an uploaded stream, then attaches it.
func (a *App) AttachUpload(ctx context.Context, postID int64, r io.Reader, originalName string) (PostView, error) {
	tmp, err := os.CreateTemp(a.originalsDir(), "upload-*.part")
	if err != nil {
		return PostView{}, err
	}
	_, err = io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return PostView{}, fmt.Errorf("store upload: %w", err)
	}
	return a.AttachFile(ctx, postID, tmp.Name(), filepath.Base(originalName), true)
}

func (a *App) DeletePost(ctx context.Context, id int64) error {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	p, err := a.Store.GetPost(ctx, id)
	if err != nil {
		return err
	}
	if p.Status == StatusPublishing {
		return conflict("post is being published right now; wait until it finishes")
	}
	assets, err := a.Store.ListAssets(ctx, id)
	if err != nil {
		return err
	}
	for _, as := range assets {
		if err := a.deleteAsset(ctx, as); err != nil {
			return err
		}
	}
	a.removeCovers(id)
	return a.Store.DeletePost(ctx, id)
}

func (a *App) GetPost(ctx context.Context, id int64) (PostView, error) {
	p, err := a.Store.GetPost(ctx, id)
	if err != nil {
		if KindOf(err) == KindNotFound {
			return PostView{}, notFound("post %d not found", id)
		}
		return PostView{}, err
	}
	return a.buildView(ctx, p)
}

// ListPosts filters by status and/or platform (enabled target).
func (a *App) ListPosts(ctx context.Context, status, platformID string) ([]PostView, error) {
	posts, err := a.Store.ListPosts(ctx, status)
	if err != nil {
		return nil, err
	}
	out := []PostView{}
	for _, p := range posts {
		v, err := a.buildView(ctx, p)
		if err != nil {
			return nil, err
		}
		if platformID != "" && !slices.ContainsFunc(v.Targets, func(t TargetView) bool { return t.Platform == platformID && t.Enabled }) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// editablePost loads a post that may still be changed.
func (a *App) editablePost(ctx context.Context, id int64) (store.Post, error) {
	p, err := a.Store.GetPost(ctx, id)
	if err != nil {
		if KindOf(err) == KindNotFound {
			return p, notFound("post %d not found", id)
		}
		return p, err
	}
	switch p.Status {
	case StatusDraft, StatusApproved, StatusFailed:
		return p, nil
	case StatusPublishing:
		return p, conflict("post %d is being published and can't be edited", id)
	default:
		return p, conflict("post %d was already published (%s) and can't be edited; create a new post instead", id, p.Status)
	}
}

// afterEdit drops the approval if the content changed, then returns the fresh view.
func (a *App) afterEdit(ctx context.Context, id int64) (PostView, error) {
	p, err := a.Store.GetPost(ctx, id)
	if err != nil {
		return PostView{}, err
	}
	v, err := a.buildView(ctx, p)
	if err != nil {
		return v, err
	}
	if (p.Status == StatusApproved && v.ContentHash != p.ApprovedHash) || p.Status == StatusFailed {
		p.Status, p.ApprovedHash = StatusDraft, ""
		if err := a.Store.UpdatePost(ctx, &p); err != nil {
			return v, err
		}
		return a.buildView(ctx, p)
	}
	return v, nil
}

func (a *App) deleteAsset(ctx context.Context, as store.Asset) error {
	rends, err := a.Store.ListRenditions(ctx, as.ID)
	if err != nil {
		return err
	}
	if err := a.Store.DeleteAsset(ctx, as.ID); err != nil { // cascades to renditions
		return err
	}
	for _, r := range rends {
		a.removeIfUnused(ctx, r.Path)
	}
	os.Remove(as.Path)
	if as.ThumbPath != "" {
		os.Remove(as.ThumbPath)
	}
	return nil
}

func (a *App) platformIDs() []string {
	var ids []string
	for _, ad := range a.Registry.All() {
		ids = append(ids, string(ad.ID()))
	}
	return ids
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
