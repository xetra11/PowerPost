package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"powerposter/internal/media"
	"powerposter/internal/platform"
	"powerposter/internal/store"
)

type transcodePayload struct {
	AssetID int64      `json:"assetId"`
	Path    string     `json:"path"`
	Plan    media.Plan `json:"plan"`
}

// ensureRenditions makes sure every enabled target has a rendition of every
// media file matching the current source and settings, queueing conversions
// where needed. Targets that need the identical output share one file and one job.
func (a *App) ensureRenditions(ctx context.Context, postID int64) error {
	a.renditionMu.Lock()
	defer a.renditionMu.Unlock()

	p, err := a.Store.GetPost(ctx, postID)
	if err != nil {
		return err
	}
	assets, err := a.mediaAssets(ctx, p)
	if err != nil || len(assets) == 0 {
		return err
	}
	targets, err := a.Store.ListTargets(ctx, postID)
	if err != nil {
		return err
	}
	strategy := media.Strategy(p.AspectStrategy)

	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		ad, ok := a.Registry.Get(platform.ID(t.Platform))
		if !ok {
			continue
		}
		c := ad.Constraints(platform.PostKind(p.Kind))
		if p.Kind == string(platform.KindImage) {
			if c.Image == nil {
				continue
			}
			// Each image keeps its own (clamped) aspect ratio unless framed to
			// another one; on SameAspect platforms the first image sets it for all.
			shared := 0.0
			if c.Image.SameAspect {
				shared = imageAspect(assets[0], t.Platform, *c.Image)
			}
			for _, as := range assets {
				aspect := shared
				if aspect == 0 {
					aspect = imageAspect(as, t.Platform, *c.Image)
				}
				plan := media.NewImagePlan(as.Info, *c.Image, strategy, aspect, frameOf(as, t.Platform))
				if err := a.ensureRendition(ctx, as, t.Platform, plan); err != nil {
					return err
				}
			}
		} else if c.Video != nil {
			plan := media.NewPlan(assets[0].Info, *c.Video, strategy, a.Cfg.FFmpegEncoder, frameOf(assets[0], t.Platform))
			if err := a.ensureRendition(ctx, assets[0], t.Platform, plan); err != nil {
				return err
			}
		}
	}
	return nil
}

// frameOf returns the hand-made framing of an asset for a platform, or nil.
func frameOf(as store.Asset, platformID string) *media.Frame {
	if f, ok := as.Framing[platformID]; ok {
		return &f
	}
	return nil
}

// imageAspect is the canvas aspect ratio of an image: the framed one, or its own (clamped).
func imageAspect(as store.Asset, platformID string, c media.ImageConstraints) float64 {
	if f := frameOf(as, platformID); f != nil && f.Aspect > 0 {
		return c.ClampAspect(f.Aspect)
	}
	return c.CanvasAspect(as.Info)
}

// ensureRendition records the rendition of one asset for one platform and
// queues its conversion unless it is up to date or another platform's job
// produces the same file. Callers hold renditionMu.
func (a *App) ensureRendition(ctx context.Context, asset store.Asset, platformID string, plan media.Plan) error {
	hash := plan.Hash()
	path := filepath.Join(a.renditionsDir(), fmt.Sprintf("%d-%s%s", asset.ID, hash[:16], plan.Ext()))

	existing, err := a.Store.ListRenditions(ctx, asset.ID)
	if err != nil {
		return err
	}
	var prev *store.Rendition
	for i := range existing {
		if existing[i].Platform == platformID {
			prev = &existing[i]
		}
	}
	if prev != nil && prev.ProfileHash == hash && prev.Status != "failed" {
		return nil // up to date or in progress
	}

	r := store.Rendition{AssetID: asset.ID, Platform: platformID, ProfileHash: hash, Plan: plan, Path: path, Status: "pending"}
	enqueue := true
	if other, err := a.Store.FindRenditionByPath(ctx, path); err == nil && other.Status != "failed" {
		if other.Status == "ready" {
			if _, statErr := os.Stat(path); statErr == nil {
				r.Status, r.Progress, r.Info, r.Checksum = "ready", 1, other.Info, other.Checksum
				enqueue = false
			}
		} else {
			r.Status, r.Progress = other.Status, other.Progress
			enqueue = false // the other target's job produces this file
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}

	if err := a.Store.UpsertRendition(ctx, &r); err != nil {
		return err
	}
	if prev != nil && prev.Path != path {
		a.removeIfUnused(ctx, prev.Path)
	}
	if enqueue {
		return a.Queue.Enqueue(ctx, jobTranscode, transcodePayload{AssetID: asset.ID, Path: path, Plan: plan})
	}
	return nil
}

func (a *App) runTranscode(ctx context.Context, raw json.RawMessage) error {
	var pl transcodePayload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return err
	}
	asset, err := a.Store.GetAsset(ctx, pl.AssetID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // post or video was removed meanwhile
	}
	if err != nil {
		return err
	}
	if n, err := a.Store.CountRenditionsByPath(ctx, pl.Path); err != nil || n == 0 {
		return err // no longer needed
	}

	a.setRenditionProgress(ctx, pl.Path, 0)
	if _, err := os.Stat(pl.Path); err != nil { // may exist when re-queued after a crash
		var err error
		if pl.Plan.Image {
			err = a.Media.ConvertImage(ctx, asset.Path, pl.Path, pl.Plan)
		} else {
			var last time.Time
			err = a.Media.Transcode(ctx, asset.Path, pl.Path, pl.Plan, asset.Info.Duration, func(f float64) {
				if time.Since(last) > time.Second {
					last = time.Now()
					a.setRenditionProgress(ctx, pl.Path, f)
				}
			})
		}
		if err != nil {
			if ctx.Err() == nil {
				a.renditionMu.Lock()
				_ = a.Store.SetRenditionFailed(ctx, pl.Path, err.Error())
				a.renditionMu.Unlock()
			}
			return err
		}
	}

	info, err := a.Media.Probe(ctx, pl.Path)
	if err == nil {
		var sum string
		if sum, err = media.Checksum(pl.Path); err == nil {
			a.renditionMu.Lock()
			err = a.Store.SetRenditionDone(ctx, pl.Path, info, sum)
			a.renditionMu.Unlock()
		}
	}
	if err != nil {
		a.renditionMu.Lock()
		_ = a.Store.SetRenditionFailed(ctx, pl.Path, err.Error())
		a.renditionMu.Unlock()
		return err
	}
	// The post may have been deleted while we were encoding.
	a.removeIfUnused(ctx, pl.Path)
	return nil
}

func (a *App) setRenditionProgress(ctx context.Context, path string, f float64) {
	a.renditionMu.Lock()
	defer a.renditionMu.Unlock()
	if err := a.Store.SetRenditionProgress(ctx, path, "processing", f); err != nil {
		a.Log.Warn("rendition progress", "err", err)
	}
}

// removeIfUnused deletes a rendition file no rendition row points to anymore.
func (a *App) removeIfUnused(ctx context.Context, path string) {
	if n, err := a.Store.CountRenditionsByPath(ctx, path); err == nil && n == 0 {
		os.Remove(path)
	}
}
