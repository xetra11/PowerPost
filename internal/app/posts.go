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
	ProjectID      int64     `json:"projectId"`
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

	if in.ProjectID == 0 {
		return PostView{}, invalid("projectId is required")
	}
	proj, err := a.project(ctx, in.ProjectID)
	if err != nil {
		return PostView{}, err
	}
	settings := a.projectSettings(proj)
	p := store.Post{
		ProjectID:      proj.ID,
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

// AttachSource is a local file to attach to a post.
type AttachSource struct {
	Path string // file to copy (or move)
	Name string // original file name; defaults to the base name of Path
	Move bool   // move instead of copy (e.g. an upload in a temp file)
}

// AttachFile attaches one local file; see AttachFiles.
func (a *App) AttachFile(ctx context.Context, postID int64, src, originalName string, move bool) (PostView, error) {
	return a.AttachFiles(ctx, postID, []AttachSource{{Path: src, Name: originalName, Move: move}}, false)
}

// AttachFiles adds media to a post. A post has either one video or images
// (several images are posted as a carousel): a video replaces everything,
// images are appended in the given order and replace a video. With replace,
// existing images are removed first. Conversion for every enabled platform
// starts right away.
func (a *App) AttachFiles(ctx context.Context, postID int64, files []AttachSource, replace bool) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	var added []store.Asset
	fail := func(err error) (PostView, error) {
		for _, as := range added {
			removeAssetFiles(as)
		}
		for _, f := range files[len(added):] {
			if f.Move {
				os.Remove(f.Path)
			}
		}
		return PostView{}, err
	}
	p, err := a.editablePost(ctx, postID)
	if err != nil {
		return fail(err)
	}
	if len(files) == 0 {
		return fail(invalid("no file given"))
	}
	for _, f := range files {
		as, err := a.importFile(ctx, postID, f)
		if err != nil {
			return fail(err)
		}
		added = append(added, as)
	}
	images := 0
	for _, as := range added {
		if as.Info.Image {
			images++
		}
	}
	if images != len(added) && len(added) > 1 {
		return fail(invalid("a post has either one video or images, not both: attach a single video, or only images"))
	}

	old, err := a.Store.ListAssets(ctx, postID)
	if err != nil {
		return fail(err)
	}
	kind := platform.KindVideo
	var keep []store.Asset
	if images > 0 {
		kind = platform.KindImage
		if p.Kind == string(platform.KindImage) && !replace {
			keep = old
		}
		if n, limit := len(keep)+len(added), a.maxImages(); n > limit {
			return fail(invalid("a post can have at most %d images (it would have %d)", limit, n))
		}
	}
	pos := 0
	if len(keep) > 0 {
		pos = keep[len(keep)-1].Position + 1
	}
	for i := range added {
		added[i].Position = pos + i
		if err := a.Store.CreateAsset(ctx, &added[i]); err != nil {
			return fail(err)
		}
	}
	for _, o := range old {
		if !slices.ContainsFunc(keep, func(k store.Asset) bool { return k.ID == o.ID }) {
			if err := a.deleteAsset(ctx, o); err != nil {
				a.Log.Warn("remove previous media", "asset", o.ID, "err", err)
			}
		}
	}

	p.Kind = string(kind)
	if kind == platform.KindVideo && float64(p.CoverMs)/1000 > added[0].Info.Duration {
		p.CoverMs = 0
	}
	if err := a.Store.UpdatePost(ctx, &p); err != nil {
		return PostView{}, err
	}
	if err := a.ensureRenditions(ctx, postID); err != nil {
		return PostView{}, err
	}
	return a.afterEdit(ctx, postID)
}

// importFile copies (or moves) a file into the media folder, probes it and
// renders its thumbnail. The asset is not stored yet.
func (a *App) importFile(ctx context.Context, postID int64, f AttachSource) (store.Asset, error) {
	name := f.Name
	if name == "" {
		name = filepath.Base(f.Path)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" || len(ext) > 6 {
		ext = ".bin"
	}
	dst := filepath.Join(a.originalsDir(), fmt.Sprintf("%d-%s%s", postID, randomHex(6), ext))
	if f.Move {
		if err := os.Rename(f.Path, dst); err != nil {
			os.Remove(f.Path)
			return store.Asset{}, err
		}
	} else if err := copyFile(f.Path, dst); err != nil {
		return store.Asset{}, fmt.Errorf("copy %s: %w", name, err)
	}

	info, err := a.Media.Probe(ctx, dst)
	if err != nil {
		os.Remove(dst)
		return store.Asset{}, invalid("%s is not a usable video or image: %v", name, err)
	}
	as := store.Asset{PostID: postID, Path: dst, OriginalName: name, Info: info}
	as.ThumbPath = filepath.Join(a.thumbsDir(), strings.TrimSuffix(filepath.Base(dst), ext)+".jpg")
	at := 0.0
	if !info.Image {
		at = min(1, info.Duration/3)
	}
	if err := a.Media.Thumbnail(ctx, dst, as.ThumbPath, at); err != nil {
		a.Log.Warn("thumbnail", "err", err)
		as.ThumbPath = ""
	}
	return as, nil
}

// maxImages is the most images any platform takes in one post.
func (a *App) maxImages() int {
	n := 0
	for _, ad := range a.Registry.All() {
		if c := ad.Constraints(platform.KindImage).Image; c != nil && slices.Contains(ad.SupportedKinds(), platform.KindImage) {
			n = max(n, c.MaxCount)
		}
	}
	if n == 0 {
		n = 10
	}
	return n
}

// AttachUpload stores an uploaded stream, then attaches it (see AttachFiles).
func (a *App) AttachUpload(ctx context.Context, postID int64, r io.Reader, originalName string, replace bool) (PostView, error) {
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
	return a.AttachFiles(ctx, postID, []AttachSource{{Path: tmp.Name(), Name: filepath.Base(originalName), Move: true}}, replace)
}

// RemoveMedia removes one image (or the video) from a post.
func (a *App) RemoveMedia(ctx context.Context, postID, assetID int64) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	if _, err := a.editablePost(ctx, postID); err != nil {
		return PostView{}, err
	}
	as, err := a.Store.GetAsset(ctx, assetID)
	if err != nil || as.PostID != postID {
		return PostView{}, notFound("media %d not found in post %d", assetID, postID)
	}
	if err := a.deleteAsset(ctx, as); err != nil {
		return PostView{}, err
	}
	// The first image may have changed, and with it the carousel's aspect ratio.
	if err := a.ensureRenditions(ctx, postID); err != nil {
		return PostView{}, err
	}
	return a.afterEdit(ctx, postID)
}

// ReorderMedia sets the order of a post's images. ids must list every image once.
func (a *App) ReorderMedia(ctx context.Context, postID int64, ids []int64) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	if _, err := a.editablePost(ctx, postID); err != nil {
		return PostView{}, err
	}
	assets, err := a.Store.ListAssets(ctx, postID)
	if err != nil {
		return PostView{}, err
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	var have []int64
	for _, as := range assets {
		have = append(have, as.ID)
	}
	slices.Sort(have)
	if !slices.Equal(sorted, have) {
		return PostView{}, invalid("list every media id of post %d exactly once (have %v)", postID, have)
	}
	for i, id := range ids {
		if err := a.Store.SetAssetPosition(ctx, id, i); err != nil {
			return PostView{}, err
		}
	}
	if err := a.ensureRenditions(ctx, postID); err != nil {
		return PostView{}, err
	}
	return a.afterEdit(ctx, postID)
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

// ListPosts filters by project (0 = all), status and/or platform (enabled target).
func (a *App) ListPosts(ctx context.Context, projectID int64, status, platformID string) ([]PostView, error) {
	posts, err := a.Store.ListPosts(ctx, projectID, status)
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
	removeAssetFiles(as)
	return nil
}

// removeAssetFiles deletes an asset's original and thumbnail.
func removeAssetFiles(as store.Asset) {
	os.Remove(as.Path)
	if as.ThumbPath != "" {
		os.Remove(as.ThumbPath)
	}
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
