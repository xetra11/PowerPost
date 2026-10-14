package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"powerposter/internal/platform"
	"powerposter/internal/store"
)

// Approve locks in the current content. Publishing is only possible while
// the content still matches what was approved.
func (a *App) Approve(ctx context.Context, id int64) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	p, err := a.editablePost(ctx, id)
	if err != nil {
		return PostView{}, err
	}
	v, err := a.buildView(ctx, p)
	if err != nil {
		return v, err
	}
	if !v.Ready {
		return v, conflict("post %d is not ready for approval: %s", id, describeBlocking(v, platform.ScopeContent))
	}
	p.Status, p.ApprovedHash = StatusApproved, v.ContentHash
	if err := a.Store.UpdatePost(ctx, &p); err != nil {
		return v, err
	}
	return a.buildView(ctx, p)
}

func (a *App) Unapprove(ctx context.Context, id int64) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	p, err := a.editablePost(ctx, id)
	if err != nil {
		return PostView{}, err
	}
	p.Status, p.ApprovedHash = StatusDraft, ""
	if err := a.Store.UpdatePost(ctx, &p); err != nil {
		return PostView{}, err
	}
	return a.buildView(ctx, p)
}

type publishPayload struct {
	PostID   int64 `json:"postId"`
	TargetID int64 `json:"targetId"`
}

// Publish queues uploads for the approved post. platforms limits which
// targets are published (empty = all enabled, not yet published ones);
// this is also how a failed target is retried.
func (a *App) Publish(ctx context.Context, id int64, platforms []string, viaMCP bool) (PostView, error) {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	if viaMCP {
		s, err := a.GetSettings(ctx)
		if err != nil {
			return PostView{}, err
		}
		if !s.MCPAllowPublish {
			return PostView{}, forbidden("publishing from Claude is turned off. Approve and publish on the review page (%s/posts/%d/review), or enable \"Allow Claude to publish\" in Settings", a.Cfg.BaseURL, id)
		}
	}
	v, err := a.GetPost(ctx, id)
	if err != nil {
		return v, err
	}
	if !v.ApprovalValid {
		return v, conflict("post %d is not approved (or changed since approval). Review and approve it first: %s", id, v.ReviewURL)
	}
	for _, p := range platforms {
		if !slices.ContainsFunc(v.Targets, func(t TargetView) bool { return t.Platform == p && t.Enabled }) {
			return v, invalid("%s is not enabled for post %d", p, id)
		}
	}

	// Only the selected platforms (all remaining ones if none are given) must be ready.
	var todo []TargetView
	var blocked []string
	for _, t := range v.Targets {
		if !t.Enabled || t.Status == "published" || t.Status == "queued" || t.Status == "publishing" {
			continue
		}
		if len(platforms) > 0 && !slices.Contains(platforms, t.Platform) {
			continue
		}
		if !t.CanPublish {
			blocked = append(blocked, t.DisplayName+": "+targetBlockers(t))
			continue
		}
		todo = append(todo, t)
	}
	if len(blocked) > 0 {
		return v, conflict("can't publish yet — %s", strings.Join(blocked, "; "))
	}
	if len(todo) == 0 {
		return v, conflict("nothing to publish: selected platforms are already published or in progress")
	}
	for _, t := range todo {
		if err := a.Store.SetTargetState(ctx, t.ID, store.TargetState{Status: "queued", Stage: "waiting"}); err != nil {
			return v, err
		}
		if err := a.Queue.Enqueue(ctx, jobPublish, publishPayload{PostID: id, TargetID: t.ID}); err != nil {
			return v, err
		}
	}
	p := v.Post
	p.Status = StatusPublishing
	if err := a.Store.UpdatePost(ctx, &p); err != nil {
		return v, err
	}
	return a.GetPost(ctx, id)
}

func (a *App) runPublish(ctx context.Context, raw json.RawMessage) error {
	var pl publishPayload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return err
	}
	v, err := a.GetPost(ctx, pl.PostID)
	if KindOf(err) == KindNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	i := slices.IndexFunc(v.Targets, func(t TargetView) bool { return t.ID == pl.TargetID })
	if i < 0 {
		return nil
	}
	tv := v.Targets[i]
	ad, _ := a.Registry.Get(platform.ID(tv.Platform))

	attemptID, err := a.Store.StartAttempt(ctx, tv.ID)
	if err != nil {
		return err
	}
	var logMu sync.Mutex
	var logBuf strings.Builder
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		fmt.Fprintf(&logBuf, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}

	result, pubErr := func() (platform.PublishResult, error) {
		if !v.ApprovalValid {
			return platform.PublishResult{}, fmt.Errorf("post changed after approval; approve it again")
		}
		if ad == nil {
			return platform.PublishResult{}, fmt.Errorf("platform %s is not available", tv.Platform)
		}
		if err := a.Store.SetTargetState(ctx, tv.ID, store.TargetState{Status: "publishing", Stage: "starting"}); err != nil {
			return platform.PublishResult{}, err
		}
		token, err := a.token(ctx, v.ProjectID, ad)
		if err != nil {
			return platform.PublishResult{}, err
		}
		pc := platform.PublishContext{
			Token: token,
			Log:   logf,
			Progress: func(stage string) {
				logf("%s", stage)
				_ = a.Store.SetTargetStage(ctx, tv.ID, stage)
			},
			PublicURL: func(path string) (string, bool) { return a.PublicMediaURL(path, 2*time.Hour) },
		}
		return ad.Publish(ctx, pc, a.resolve(v.Post, tv))
	}()

	if ctx.Err() != nil {
		return ctx.Err() // shutdown: recovered on next start
	}
	if pubErr != nil {
		logf("failed: %v", pubErr)
		_ = a.Store.SetTargetState(ctx, tv.ID, store.TargetState{Status: "failed", RemoteID: result.RemoteID, Error: pubErr.Error()})
		_ = a.Store.FinishAttempt(ctx, attemptID, "failed", pubErr.Error(), logBuf.String())
	} else {
		logf("done %s %s", result.RemoteID, result.Permalink)
		now := time.Now()
		_ = a.Store.SetTargetState(ctx, tv.ID, store.TargetState{Status: "published", RemoteID: result.RemoteID, Permalink: result.Permalink, Note: result.Note, PublishedAt: &now})
		_ = a.Store.FinishAttempt(ctx, attemptID, "succeeded", "", logBuf.String())
	}
	if err := a.updatePostStatus(ctx, pl.PostID); err != nil {
		return err
	}
	return pubErr
}

// updatePostStatus derives the post status from its targets after publishing.
func (a *App) updatePostStatus(ctx context.Context, postID int64) error {
	a.postMu.Lock()
	defer a.postMu.Unlock()

	counts, err := a.Store.CountTargetsByStatus(ctx, postID)
	if err != nil {
		return err
	}
	p, err := a.Store.GetPost(ctx, postID)
	if err != nil {
		return err
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	switch {
	case counts["queued"]+counts["publishing"] > 0:
		p.Status = StatusPublishing
	case counts["published"] == total:
		p.Status = StatusPublished
	case counts["published"] > 0:
		p.Status = StatusPartial
	case counts["failed"] > 0:
		p.Status = StatusFailed
	default:
		p.Status = StatusApproved
	}
	return a.Store.UpdatePost(ctx, &p)
}

func (a *App) recoverPublish(ctx context.Context, raw json.RawMessage) {
	var pl publishPayload
	if json.Unmarshal(raw, &pl) != nil {
		return
	}
	msg := "Interrupted by a restart. Check the platform before retrying — the post may already be live."
	_ = a.Store.SetTargetState(ctx, pl.TargetID, store.TargetState{Status: "failed", Error: msg})
	if err := a.updatePostStatus(ctx, pl.PostID); err != nil {
		a.Log.Warn("recover publish", "post", pl.PostID, "err", err)
	}
}

func describeBlocking(v PostView, scope platform.Scope) string {
	var msgs []string
	for _, i := range v.Issues {
		if i.Severity != platform.SeverityError || (scope != "" && i.Scope != scope) {
			continue
		}
		prefix := ""
		if i.Platform != "" {
			prefix = string(i.Platform) + ": "
		}
		msgs = append(msgs, prefix+i.Message)
	}
	for _, t := range v.Targets {
		if t.Enabled && len(v.Assets) > 0 && !t.converted(len(v.Assets)) &&
			!slices.ContainsFunc(t.Renditions, func(r RenditionView) bool { return r.Status == "failed" }) {
			msgs = append(msgs, t.Platform+": conversion still running")
		}
	}
	if len(msgs) == 0 {
		return "unknown reason"
	}
	return strings.Join(msgs, "; ")
}

// targetBlockers explains why a single platform can't be published.
func targetBlockers(t TargetView) string {
	var msgs []string
	for _, i := range t.Issues {
		if i.Severity == platform.SeverityError {
			msgs = append(msgs, i.Message)
		}
	}
	if len(msgs) == 0 {
		return "not ready"
	}
	return strings.Join(msgs, " ")
}

// CheckLive asks the platforms whether published posts still exist. Targets
// deleted in the app are marked "removed" and can be published again.
func (a *App) CheckLive(ctx context.Context, id int64) (PostView, error) {
	v, err := a.GetPost(ctx, id)
	if err != nil {
		return v, err
	}
	changed := false
	for _, t := range v.Targets {
		if t.Status != "published" || t.RemoteID == "" {
			continue
		}
		ad, ok := a.Registry.Get(platform.ID(t.Platform))
		checker, can := ad.(platform.LiveChecker)
		if !ok || !can {
			continue
		}
		token, err := a.token(ctx, v.ProjectID, ad)
		if err != nil {
			return v, err
		}
		live, err := checker.IsLive(ctx, token, t.RemoteID)
		if err != nil {
			return v, fmt.Errorf("%s: %w", ad.DisplayName(), err)
		}
		if !live {
			note := fmt.Sprintf("Deleted on %s. You can change the post and publish it again.", ad.DisplayName())
			if err := a.Store.SetTargetState(ctx, t.ID, store.TargetState{Status: "removed", RemoteID: t.RemoteID, Note: note}); err != nil {
				return v, err
			}
			changed = true
		}
	}
	if !changed {
		return v, nil
	}
	if err := a.updatePostStatus(ctx, id); err != nil {
		return v, err
	}
	return a.GetPost(ctx, id)
}

// Stats fetches engagement numbers for the published platforms of a post.
// Platform errors are reported in the stats' note instead of failing the call.
func (a *App) Stats(ctx context.Context, id int64) (map[string]platform.Stats, error) {
	v, err := a.GetPost(ctx, id)
	if err != nil {
		return nil, err
	}
	out := map[string]platform.Stats{}
	for _, t := range v.Targets {
		if t.Status != "published" || t.RemoteID == "" {
			continue
		}
		ad, ok := a.Registry.Get(platform.ID(t.Platform))
		provider, can := ad.(platform.StatsProvider)
		if !ok || !can {
			continue
		}
		token, err := a.token(ctx, v.ProjectID, ad)
		if err != nil {
			out[t.Platform] = platform.Stats{Note: err.Error(), At: time.Now()}
			continue
		}
		s, err := provider.Stats(ctx, token, t.RemoteID)
		if err != nil {
			s = platform.Stats{Note: "Couldn't load numbers: " + err.Error(), At: time.Now()}
		}
		out[t.Platform] = s
	}
	return out, nil
}
