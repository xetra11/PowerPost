package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"powerposter/internal/store"
)

// DuplicatePost copies a post into a new draft: text, options, cover, the
// video and its finished conversions (copied, so nothing is re-encoded).
// Only platforms that aren't live on the original are enabled in the copy,
// so a post that was published on TikTok but removed on Instagram can be
// fixed and re-posted to Instagram alone. If every platform is live, the
// copy keeps the original selection.
func (a *App) DuplicatePost(ctx context.Context, id int64) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	src, err := a.Store.GetPost(ctx, id)
	if err != nil {
		if KindOf(err) == KindNotFound {
			return PostView{}, notFound("post %d not found", id)
		}
		return PostView{}, err
	}
	targets, err := a.Store.ListTargets(ctx, id)
	if err != nil {
		return PostView{}, err
	}
	assets, err := a.Store.ListAssets(ctx, id)
	if err != nil {
		return PostView{}, err
	}

	p := store.Post{
		ProjectID:      src.ProjectID,
		Title:          copyTitle(src.Title),
		Kind:           src.Kind,
		Caption:        src.Caption,
		Hashtags:       src.Hashtags,
		AspectStrategy: src.AspectStrategy,
		CoverMs:        src.CoverMs,
		Status:         StatusDraft,
		CreatedBy:      "ui",
	}
	if err := a.Store.CreatePost(ctx, &p); err != nil {
		return PostView{}, err
	}
	if src.CoverCustom && src.CoverPath != "" {
		dst := filepath.Join(a.coversDir(), fmt.Sprintf("%d-c%s.jpg", p.ID, randomHex(6)))
		if err := copyFile(src.CoverPath, dst); err == nil {
			p.CoverPath, p.CoverCustom = dst, true
			if err := a.Store.UpdatePost(ctx, &p); err != nil {
				return PostView{}, err
			}
		}
	}

	notLive := func(t store.Target) bool { return t.Enabled && t.Status != "published" }
	onlyNotLive := false
	for _, t := range targets {
		onlyNotLive = onlyNotLive || notLive(t)
	}
	for _, t := range targets {
		c := store.Target{
			PostID: p.ID, Platform: t.Platform, Options: t.Options,
			CaptionOverride: t.CaptionOverride, HashtagsOverride: t.HashtagsOverride,
			Enabled: t.Enabled && (!onlyNotLive || notLive(t)),
		}
		if err := a.Store.UpsertTarget(ctx, &c); err != nil {
			return PostView{}, err
		}
	}

	for _, as := range assets {
		if err := a.copyAsset(ctx, as, p.ID); err != nil {
			a.deletePostFiles(ctx, p.ID)
			_ = a.Store.DeletePost(ctx, p.ID)
			return PostView{}, fmt.Errorf("copy media: %w", err)
		}
	}
	if len(assets) > 0 {
		if err := a.ensureRenditions(ctx, p.ID); err != nil {
			return PostView{}, err
		}
	}
	return a.GetPost(ctx, p.ID)
}

// copyAsset copies the video, thumbnail and ready renditions to a new post.
func (a *App) copyAsset(ctx context.Context, as store.Asset, postID int64) error {
	ext := filepath.Ext(as.Path)
	dst := store.Asset{PostID: postID, OriginalName: as.OriginalName, Info: as.Info, Position: as.Position, Framing: as.Framing,
		Path: filepath.Join(a.originalsDir(), fmt.Sprintf("%d-%s%s", postID, randomHex(6), ext))}
	if err := copyFile(as.Path, dst.Path); err != nil {
		return err
	}
	if as.ThumbPath != "" {
		dst.ThumbPath = filepath.Join(a.thumbsDir(), strings.TrimSuffix(filepath.Base(dst.Path), ext)+".jpg")
		if copyFile(as.ThumbPath, dst.ThumbPath) != nil {
			dst.ThumbPath = ""
		}
	}
	if err := a.Store.CreateAsset(ctx, &dst); err != nil {
		os.Remove(dst.Path)
		return err
	}

	rends, err := a.Store.ListRenditions(ctx, as.ID)
	if err != nil {
		return err
	}
	a.renditionMu.Lock()
	defer a.renditionMu.Unlock()
	copied := map[string]string{} // platforms that share a file keep sharing it
	for _, r := range rends {
		if r.Status != "ready" {
			continue // ensureRenditions converts these again
		}
		newPath, ok := copied[r.Path]
		if !ok {
			newPath = filepath.Join(a.renditionsDir(), fmt.Sprintf("%d-%s%s", dst.ID, r.ProfileHash[:16], r.Plan.Ext()))
			if err := copyFile(r.Path, newPath); err != nil {
				continue
			}
			copied[r.Path] = newPath
		}
		c := store.Rendition{AssetID: dst.ID, Platform: r.Platform, ProfileHash: r.ProfileHash, Plan: r.Plan,
			Path: newPath, Info: r.Info, Status: "ready", Progress: 1, Checksum: r.Checksum}
		if err := a.Store.UpsertRendition(ctx, &c); err != nil {
			return err
		}
	}
	return nil
}

// deletePostFiles removes the files of a post (used to undo a failed copy).
func (a *App) deletePostFiles(ctx context.Context, postID int64) {
	if assets, err := a.Store.ListAssets(ctx, postID); err == nil {
		for _, as := range assets {
			_ = a.deleteAsset(ctx, as)
		}
	}
	a.removeCovers(postID)
}

func copyTitle(t string) string {
	if strings.HasSuffix(t, "(copy)") {
		return t
	}
	return t + " (copy)"
}
