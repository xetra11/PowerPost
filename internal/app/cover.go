package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"powerposter/internal/platform"
	"powerposter/internal/store"
)

// Covers: by default a post's cover is a frame of the converted video at
// CoverMs (Instagram thumb_offset, TikTok video_cover_timestamp_ms). Instagram
// can also use an uploaded image (cover_url); TikTok's API can't.

func (a *App) coversDir() string { return filepath.Join(a.Cfg.DataDir, "media", "covers") }

// coverURL is the preview image for the post's cover, or "" if none yet.
func coverURL(p store.Post, targets []TargetView) string {
	if p.CoverCustom && p.CoverPath != "" && p.Kind != string(platform.KindImage) {
		return mediaURL("covers", p.CoverPath)
	}
	return coverFrameURL(p, targets)
}

// coverFrameURL previews the video frame at CoverMs (what TikTok always uses).
// Image posts have no cover: the first image is shown first.
func coverFrameURL(p store.Post, targets []TargetView) string {
	if p.Kind == string(platform.KindImage) {
		return ""
	}
	for _, t := range targets {
		if r := t.Rendition; t.Enabled && r != nil && r.Status == "ready" && len(r.Checksum) >= 12 {
			return fmt.Sprintf("/media/covers/%d-f%d-%s.jpg", p.ID, p.CoverMs, r.Checksum[:12])
		}
	}
	return ""
}

// CoverFile returns a cover image, rendering frame previews on first request.
func (a *App) CoverFile(ctx context.Context, name string) (string, error) {
	name = filepath.Base(name)
	path := filepath.Join(a.coversDir(), name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	// Frame previews are named <postID>-f<ms>-<rendition checksum prefix>.jpg.
	parts := strings.SplitN(strings.TrimSuffix(name, ".jpg"), "-", 3)
	if len(parts) != 3 || !strings.HasPrefix(parts[1], "f") {
		return "", notFound("no such cover")
	}
	postID, err1 := strconv.ParseInt(parts[0], 10, 64)
	ms, err2 := strconv.ParseInt(parts[1][1:], 10, 64)
	if err1 != nil || err2 != nil {
		return "", notFound("no such cover")
	}
	v, err := a.GetPost(ctx, postID)
	if err != nil {
		return "", err
	}
	for _, t := range v.Targets {
		if r := t.Rendition; r != nil && r.Status == "ready" && strings.HasPrefix(r.Checksum, parts[2]) {
			if err := a.Media.Frame(ctx, r.Path, path, float64(ms)/1000); err != nil {
				return "", err
			}
			return path, nil
		}
	}
	return "", notFound("no such cover")
}

// SetCoverImage stores an uploaded image as the post's custom cover (Instagram).
func (a *App) SetCoverImage(ctx context.Context, id int64, r io.Reader, originalName string) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	p, err := a.editablePost(ctx, id)
	if err != nil {
		return PostView{}, err
	}
	if p.Kind == string(platform.KindImage) {
		return PostView{}, invalid("image posts have no cover image; the first image is shown first (reorder the images instead)")
	}
	if err := os.MkdirAll(a.coversDir(), 0o755); err != nil {
		return PostView{}, err
	}
	tmp, err := os.CreateTemp(a.coversDir(), "upload-*"+strings.ToLower(filepath.Ext(originalName)))
	if err != nil {
		return PostView{}, err
	}
	_, err = io.Copy(tmp, r)
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err != nil {
		return PostView{}, err
	}
	out := filepath.Join(a.coversDir(), fmt.Sprintf("%d-c%s.jpg", id, randomHex(6)))
	if err := a.Media.CoverImage(ctx, tmp.Name(), out); err != nil {
		return PostView{}, invalid("%s is not a usable image: %v", filepath.Base(originalName), err)
	}
	if p.CoverCustom && p.CoverPath != "" {
		os.Remove(p.CoverPath)
	}
	p.CoverPath, p.CoverCustom = out, true
	if err := a.Store.UpdatePost(ctx, &p); err != nil {
		return PostView{}, err
	}
	return a.afterEdit(ctx, id)
}

// ClearCoverImage goes back to using a video frame as cover.
func (a *App) ClearCoverImage(ctx context.Context, id int64) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	p, err := a.editablePost(ctx, id)
	if err != nil {
		return PostView{}, err
	}
	if p.CoverCustom && p.CoverPath != "" {
		os.Remove(p.CoverPath)
	}
	p.CoverPath, p.CoverCustom = "", false
	if err := a.Store.UpdatePost(ctx, &p); err != nil {
		return PostView{}, err
	}
	return a.afterEdit(ctx, id)
}

// removeCovers deletes all cover files of a post.
func (a *App) removeCovers(id int64) {
	files, _ := filepath.Glob(filepath.Join(a.coversDir(), fmt.Sprintf("%d-*.jpg", id)))
	for _, f := range files {
		os.Remove(f)
	}
}
