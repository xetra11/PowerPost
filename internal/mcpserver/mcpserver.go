// Package mcpserver exposes PowerPoster to Claude over MCP (Streamable HTTP).
// Tools call the same app services as the web UI, so Claude can prepare
// posts but never skip validation or the human review step.
package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"powerposter/internal/app"
	"powerposter/internal/platform"
)

const instructions = `PowerPoster prepares and publishes social media posts (Instagram Reels, TikTok videos) for a game studio.

Workflow:
1. get_platform_constraints / read powerposter://brand to learn limits and the brand voice.
2. create_post with caption, hashtags and target platforms.
3. attach_media with a video file saved inside the PowerPoster inbox folder (see list_platforms → inboxHostPath). PowerPoster converts it for each platform automatically.
4. validate_post until "ready" is true (conversion runs in the background; poll every few seconds).
5. Give the user the review link (get_review_link). The user approves on the review page.
6. publish_post only works after the user approved the post, and only if they enabled "Allow Claude to publish" in Settings.

Never claim a post was published unless get_publish_status reports "published".`

// New builds the MCP server with all tools and resources.
func New(a *app.App) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "powerposter", Title: "PowerPoster", Version: "0.1.0"}, &mcp.ServerOptions{Instructions: instructions})
	t := tools{a}

	mcp.AddTool(s, &mcp.Tool{Name: "list_platforms", Description: "List supported platforms with connection status, the inbox folder for attach_media, and each platform's options schema."}, t.listPlatforms)
	mcp.AddTool(s, &mcp.Tool{Name: "get_platform_constraints", Description: "Media and caption limits for a platform (resolution, fps, duration, size, caption length). Use it to produce fitting media up front."}, t.getConstraints)
	mcp.AddTool(s, &mcp.Tool{Name: "create_post", Description: "Create a draft post. Hashtags default to the brand's default hashtags; platforms default to the user's default platforms."}, t.createPost)
	mcp.AddTool(s, &mcp.Tool{Name: "attach_media", Description: "Attach a video file to a post (replaces any existing video). The path must be inside the PowerPoster inbox folder. Conversion for each platform starts automatically."}, t.attachMedia)
	mcp.AddTool(s, &mcp.Tool{Name: "upload_media", Description: "Attach a small video (max 50 MB) by sending its content as base64. Prefer attach_media for files in the inbox."}, t.uploadMedia)
	mcp.AddTool(s, &mcp.Tool{Name: "set_cover_image", Description: "Use an image from the inbox folder as custom cover (Instagram; converted to 1080x1920). TikTok always uses the video frame at cover_ms. Pass an empty path to go back to the video frame."}, t.setCover)
	mcp.AddTool(s, &mcp.Tool{Name: "duplicate_post", Description: "Copy a post (text, options, cover, video incl. conversions) into a new draft. Only platforms not yet live on the original are enabled. Use it to re-post or change a post that was already (partly) published."}, t.duplicatePost)
	mcp.AddTool(s, &mcp.Tool{Name: "update_post", Description: "Edit a draft: title, caption, hashtags, aspect strategy (blur|crop|pad) or cover frame time. Any change after approval sends the post back to draft."}, t.updatePost)
	mcp.AddTool(s, &mcp.Tool{Name: "set_target_options", Description: "Per-platform settings: enable/disable a platform, caption or hashtag overrides, and platform options (see the options schema in list_platforms)."}, t.setTarget)
	mcp.AddTool(s, &mcp.Tool{Name: "validate_post", Description: "Validation result per platform: errors, warnings (incl. automatic fixes like downscaling), conversion progress, and whether the post is ready for review."}, t.validatePost)
	mcp.AddTool(s, &mcp.Tool{Name: "get_post", Description: "Full state of a post."}, t.getPost)
	mcp.AddTool(s, &mcp.Tool{Name: "list_posts", Description: "List posts, newest first. Optional filters: status (draft|approved|publishing|published|partial|failed) and platform."}, t.listPosts)
	mcp.AddTool(s, &mcp.Tool{Name: "get_review_link", Description: "URL of the review page where the user previews the converted files and approves the post."}, t.reviewLink)
	mcp.AddTool(s, &mcp.Tool{Name: "publish_post", Description: "Publish an approved post. Fails unless the user approved it in the UI and enabled \"Allow Claude to publish\" in Settings."}, t.publishPost)
	mcp.AddTool(s, &mcp.Tool{Name: "get_post_stats", Description: "Engagement of a published post per platform (likes, comments, views where the platform provides them)."}, t.postStats)
	mcp.AddTool(s, &mcp.Tool{Name: "get_publish_status", Description: "Per-platform publish progress, permalinks and errors."}, t.publishStatus)

	s.AddResource(&mcp.Resource{URI: "powerposter://brand", Name: "brand", Title: "Brand profile", Description: "Game name, link, default hashtags and tone notes for captions.", MIMEType: "application/json"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			st, err := a.GetSettings(ctx)
			if err != nil {
				return nil, err
			}
			return jsonResource(req.Params.URI, st.Brand)
		})
	for _, ad := range a.Registry.All() {
		uri := fmt.Sprintf("powerposter://platforms/%s/constraints", ad.ID())
		s.AddResource(&mcp.Resource{URI: uri, Name: string(ad.ID()) + "-constraints", Title: ad.DisplayName() + " limits", MIMEType: "application/json"},
			func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return jsonResource(uri, ad.Constraints(platform.KindVideo))
			})
	}
	return s
}

// Handler serves MCP over Streamable HTTP.
func Handler(a *app.App) http.Handler {
	srv := New(a)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true})
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: string(b)}}}, nil
}

type tools struct{ a *app.App }

// ---- inputs ----

type empty struct{}

type platformIn struct {
	Platform string `json:"platform" jsonschema:"platform id, e.g. instagram or tiktok"`
}

type postIn struct {
	PostID int64 `json:"post_id" jsonschema:"the post id"`
}

type createPostIn struct {
	Title          string    `json:"title,omitempty" jsonschema:"internal title shown in the post list"`
	Caption        string    `json:"caption,omitempty" jsonschema:"caption text without hashtags"`
	Hashtags       *[]string `json:"hashtags,omitempty" jsonschema:"hashtags without '#'; omit to use the brand defaults"`
	Platforms      []string  `json:"platforms,omitempty" jsonschema:"platform ids to post to; omit for the user's defaults"`
	AspectStrategy string    `json:"aspect_strategy,omitempty" jsonschema:"how non-9:16 video fills the frame: blur (default), crop or pad"`
}

type attachIn struct {
	PostID int64  `json:"post_id" jsonschema:"the post id"`
	Path   string `json:"path" jsonschema:"absolute path of the video inside the inbox folder"`
}

type uploadIn struct {
	PostID        int64  `json:"post_id" jsonschema:"the post id"`
	Filename      string `json:"filename" jsonschema:"file name including extension, e.g. reel.mp4"`
	ContentBase64 string `json:"content_base64" jsonschema:"file content, base64 encoded"`
}

type coverIn struct {
	PostID int64  `json:"post_id" jsonschema:"the post id"`
	Path   string `json:"path" jsonschema:"absolute path of the image inside the inbox folder; empty to use the video frame"`
}

type updatePostIn struct {
	PostID         int64     `json:"post_id" jsonschema:"the post id"`
	Title          *string   `json:"title,omitempty"`
	Caption        *string   `json:"caption,omitempty" jsonschema:"caption text without hashtags"`
	Hashtags       *[]string `json:"hashtags,omitempty" jsonschema:"replaces the hashtag list"`
	AspectStrategy *string   `json:"aspect_strategy,omitempty" jsonschema:"blur, crop or pad"`
	CoverMs        *int64    `json:"cover_ms,omitempty" jsonschema:"cover frame position in milliseconds"`
}

type setTargetIn struct {
	PostID           int64          `json:"post_id" jsonschema:"the post id"`
	Platform         string         `json:"platform" jsonschema:"platform id"`
	Enabled          *bool          `json:"enabled,omitempty" jsonschema:"post to this platform or not"`
	CaptionOverride  *string        `json:"caption_override,omitempty" jsonschema:"platform-specific caption (without hashtags)"`
	UseBaseCaption   bool           `json:"use_base_caption,omitempty" jsonschema:"remove the caption override"`
	HashtagsOverride *[]string      `json:"hashtags_override,omitempty" jsonschema:"platform-specific hashtags"`
	UseBaseHashtags  bool           `json:"use_base_hashtags,omitempty" jsonschema:"remove the hashtag override"`
	Options          map[string]any `json:"options,omitempty" jsonschema:"platform options to change (keys from the platform's options schema)"`
}

type listIn struct {
	Status   string `json:"status,omitempty" jsonschema:"filter by status"`
	Platform string `json:"platform,omitempty" jsonschema:"filter by enabled platform"`
}

type publishIn struct {
	PostID    int64    `json:"post_id" jsonschema:"the post id"`
	Platforms []string `json:"platforms,omitempty" jsonschema:"only these platforms (e.g. to retry a failed one); omit for all"`
}

// ---- handlers ----

func (t tools) listPlatforms(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
	list, err := t.a.Platforms(ctx)
	if err != nil {
		return nil, nil, err
	}
	type pv struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Configured    bool   `json:"configured"`
		Connected     bool   `json:"connected"`
		Account       string `json:"account,omitempty"`
		OptionsSchema any    `json:"optionsSchema"`
	}
	out := struct {
		InboxHostPath string `json:"inboxHostPath"`
		Platforms     []pv   `json:"platforms"`
	}{InboxHostPath: t.a.Cfg.InboxHostPath}
	for _, p := range list {
		out.Platforms = append(out.Platforms, pv{p.ID, p.DisplayName, p.Configured, p.Connected, p.AccountName, p.OptionsSchema})
	}
	return nil, out, nil
}

func (t tools) getConstraints(ctx context.Context, _ *mcp.CallToolRequest, in platformIn) (*mcp.CallToolResult, any, error) {
	ad, ok := t.a.Registry.Get(platform.ID(in.Platform))
	if !ok {
		return nil, nil, fmt.Errorf("unknown platform %q", in.Platform)
	}
	return nil, map[string]any{
		"platform":      ad.ID(),
		"constraints":   ad.Constraints(platform.KindVideo),
		"optionsSchema": ad.OptionsSchema(),
		"note":          "PowerPoster converts videos to targetWidth x targetHeight automatically; durations outside the limits must be trimmed before attaching.",
	}, nil
}

func (t tools) createPost(ctx context.Context, _ *mcp.CallToolRequest, in createPostIn) (*mcp.CallToolResult, any, error) {
	v, err := t.a.CreatePost(ctx, app.CreatePostInput{
		Title: in.Title, Caption: in.Caption, Hashtags: in.Hashtags, Platforms: in.Platforms,
		AspectStrategy: in.AspectStrategy, CreatedBy: "mcp",
	})
	return post(v, err)
}

func (t tools) attachMedia(ctx context.Context, _ *mcp.CallToolRequest, in attachIn) (*mcp.CallToolResult, any, error) {
	local, err := t.a.ResolveInboxPath(in.Path)
	if err != nil {
		return nil, nil, err
	}
	v, err := t.a.AttachFile(ctx, in.PostID, local, "", false)
	return post(v, err)
}

const maxUpload = 50 << 20

func (t tools) uploadMedia(ctx context.Context, _ *mcp.CallToolRequest, in uploadIn) (*mcp.CallToolResult, any, error) {
	if base64.StdEncoding.DecodedLen(len(in.ContentBase64)) > maxUpload {
		return nil, nil, fmt.Errorf("file too large for upload_media (max 50 MB); save it into the inbox folder and use attach_media")
	}
	data, err := base64.StdEncoding.DecodeString(in.ContentBase64)
	if err != nil {
		return nil, nil, fmt.Errorf("content_base64 is not valid base64: %v", err)
	}
	v, err := t.a.AttachUpload(ctx, in.PostID, bytes.NewReader(data), in.Filename)
	return post(v, err)
}

func (t tools) setCover(ctx context.Context, _ *mcp.CallToolRequest, in coverIn) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Path) == "" {
		return post(t.a.ClearCoverImage(ctx, in.PostID))
	}
	local, err := t.a.ResolveInboxPath(in.Path)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(local)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	return post(t.a.SetCoverImage(ctx, in.PostID, f, filepath.Base(local)))
}

func (t tools) duplicatePost(ctx context.Context, _ *mcp.CallToolRequest, in postIn) (*mcp.CallToolResult, any, error) {
	return post(t.a.DuplicatePost(ctx, in.PostID))
}

func (t tools) updatePost(ctx context.Context, _ *mcp.CallToolRequest, in updatePostIn) (*mcp.CallToolResult, any, error) {
	v, err := t.a.UpdatePost(ctx, in.PostID, app.PostPatch{
		Title: in.Title, Caption: in.Caption, Hashtags: in.Hashtags, AspectStrategy: in.AspectStrategy, CoverMs: in.CoverMs,
	})
	return post(v, err)
}

func (t tools) setTarget(ctx context.Context, _ *mcp.CallToolRequest, in setTargetIn) (*mcp.CallToolResult, any, error) {
	patch := app.TargetPatch{
		Enabled: in.Enabled, CaptionOverride: in.CaptionOverride, UseBaseCaption: in.UseBaseCaption,
		HashtagsOverride: in.HashtagsOverride, UseBaseHashtags: in.UseBaseHashtags,
	}
	if len(in.Options) > 0 {
		b, err := json.Marshal(in.Options)
		if err != nil {
			return nil, nil, err
		}
		patch.Options = b
	}
	v, err := t.a.UpdateTarget(ctx, in.PostID, in.Platform, patch)
	return post(v, err)
}

func (t tools) validatePost(ctx context.Context, _ *mcp.CallToolRequest, in postIn) (*mcp.CallToolResult, any, error) {
	v, err := t.a.GetPost(ctx, in.PostID)
	if err != nil {
		return nil, nil, err
	}
	s := summarize(v)
	return nil, map[string]any{
		"postId":        v.ID,
		"ready":         v.Ready,
		"approvalValid": v.ApprovalValid,
		"status":        v.Status,
		"targets":       s.Targets,
		"reviewUrl":     v.ReviewURL,
	}, nil
}

func (t tools) getPost(ctx context.Context, _ *mcp.CallToolRequest, in postIn) (*mcp.CallToolResult, any, error) {
	return post(t.a.GetPost(ctx, in.PostID))
}

func (t tools) listPosts(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, any, error) {
	list, err := t.a.ListPosts(ctx, in.Status, in.Platform)
	if err != nil {
		return nil, nil, err
	}
	type item struct {
		ID        int64    `json:"id"`
		Title     string   `json:"title"`
		Status    string   `json:"status"`
		Platforms []string `json:"platforms"`
		Updated   string   `json:"updated"`
		ReviewURL string   `json:"reviewUrl"`
	}
	out := []item{}
	for _, v := range list {
		it := item{ID: v.ID, Title: v.Title, Status: v.Status, Platforms: []string{}, Updated: v.UpdatedAt.Local().Format("2006-01-02 15:04"), ReviewURL: v.ReviewURL}
		for _, tg := range v.Targets {
			if tg.Enabled {
				it.Platforms = append(it.Platforms, tg.Platform+":"+tg.Status)
			}
		}
		out = append(out, it)
	}
	return nil, map[string]any{"posts": out}, nil
}

func (t tools) reviewLink(ctx context.Context, _ *mcp.CallToolRequest, in postIn) (*mcp.CallToolResult, any, error) {
	v, err := t.a.GetPost(ctx, in.PostID)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"reviewUrl": v.ReviewURL, "editUrl": v.EditURL, "ready": v.Ready}, nil
}

func (t tools) publishPost(ctx context.Context, _ *mcp.CallToolRequest, in publishIn) (*mcp.CallToolResult, any, error) {
	v, err := t.a.Publish(ctx, in.PostID, in.Platforms, true)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"status": v.Status, "message": "Publishing started. Check get_publish_status.", "targets": summarize(v).Targets}, nil
}

func (t tools) postStats(ctx context.Context, _ *mcp.CallToolRequest, in postIn) (*mcp.CallToolResult, any, error) {
	st, err := t.a.Stats(ctx, in.PostID)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"postId": in.PostID, "stats": st}, nil
}

func (t tools) publishStatus(ctx context.Context, _ *mcp.CallToolRequest, in postIn) (*mcp.CallToolResult, any, error) {
	v, err := t.a.GetPost(ctx, in.PostID)
	if err != nil {
		return nil, nil, err
	}
	type st struct {
		Platform  string `json:"platform"`
		Status    string `json:"status"`
		Stage     string `json:"stage,omitempty"`
		Permalink string `json:"permalink,omitempty"`
		Note      string `json:"note,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	out := []st{}
	for _, tg := range v.Targets {
		if tg.Enabled {
			out = append(out, st{tg.Platform, tg.Status, tg.Stage, tg.Permalink, tg.Note, tg.Error})
		}
	}
	return nil, map[string]any{"postId": v.ID, "status": v.Status, "targets": out}, nil
}

// ---- output shaping ----

type issueOut struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	AutoFix  string `json:"autoFix,omitempty"`
}

type renditionOut struct {
	Status   string   `json:"status"`
	Progress int      `json:"progressPercent"`
	Width    int      `json:"width,omitempty"`
	Height   int      `json:"height,omitempty"`
	FPS      float64  `json:"fps,omitempty"`
	SizeMB   float64  `json:"sizeMb,omitempty"`
	Fixes    []string `json:"fixes,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type targetOut struct {
	Platform     string          `json:"platform"`
	Enabled      bool            `json:"enabled"`
	Status       string          `json:"publishStatus,omitempty"`
	FinalCaption string          `json:"finalCaption,omitempty"`
	CaptionChars string          `json:"captionChars,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"`
	Rendition    *renditionOut   `json:"rendition,omitempty"`
	Issues       []issueOut      `json:"issues,omitempty"`
	Permalink    string          `json:"permalink,omitempty"`
	Error        string          `json:"error,omitempty"`
}

type postOut struct {
	ID             int64       `json:"id"`
	Title          string      `json:"title"`
	Status         string      `json:"status"`
	Caption        string      `json:"caption"`
	Hashtags       []string    `json:"hashtags"`
	AspectStrategy string      `json:"aspectStrategy"`
	CoverMs        int64       `json:"coverMs"`
	Video          *videoOut   `json:"video,omitempty"`
	Ready          bool        `json:"ready"`
	ApprovalValid  bool        `json:"approvalValid"`
	CanPublish     bool        `json:"canPublish"`
	Targets        []targetOut `json:"targets"`
	ReviewURL      string      `json:"reviewUrl"`
}

type videoOut struct {
	Name     string  `json:"name"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	FPS      float64 `json:"fps"`
	Duration float64 `json:"durationSec"`
}

func summarize(v app.PostView) postOut {
	o := postOut{
		ID: v.ID, Title: v.Title, Status: v.Status, Caption: v.Caption, Hashtags: v.Hashtags,
		AspectStrategy: v.AspectStrategy, CoverMs: v.CoverMs, Ready: v.Ready, ApprovalValid: v.ApprovalValid,
		CanPublish: v.CanPublish, ReviewURL: v.ReviewURL, Targets: []targetOut{},
	}
	if v.Asset != nil {
		i := v.Asset.Info
		o.Video = &videoOut{v.Asset.OriginalName, i.Width, i.Height, i.FPS, i.Duration}
	}
	for _, t := range v.Targets {
		to := targetOut{Platform: t.Platform, Enabled: t.Enabled}
		if t.Enabled {
			to.Status = t.Status
			to.FinalCaption = t.FinalCaption
			to.CaptionChars = fmt.Sprintf("%d/%d", t.CaptionLength, t.CaptionMax)
			to.Options = t.Options
			to.Permalink = t.Permalink
			to.Error = t.Error
			if r := t.Rendition; r != nil {
				ro := &renditionOut{Status: r.Status, Progress: int(r.Progress * 100), Fixes: r.Plan.Fixes, Error: r.Error}
				if r.Info != nil {
					ro.Width, ro.Height, ro.FPS = r.Info.Width, r.Info.Height, r.Info.FPS
					ro.SizeMB = float64(r.Info.Size*10/(1<<20)) / 10
				}
				to.Rendition = ro
			}
			for _, is := range t.Issues {
				if is.Code == "AUTO_FIXED" {
					continue // listed under rendition.fixes
				}
				to.Issues = append(to.Issues, issueOut{string(is.Severity), is.Message, is.AutoFix})
			}
		}
		o.Targets = append(o.Targets, to)
	}
	return o
}

func post(v app.PostView, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	return nil, summarize(v), nil
}
