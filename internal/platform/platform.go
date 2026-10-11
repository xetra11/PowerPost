// Package platform defines the adapter contract every social platform implements.
// Adding a platform = one new sub-package + one line in the registry.
package platform

import (
	"context"
	"encoding/json"
	"time"

	"powerposter/internal/media"
)

type ID string

type PostKind string

const (
	KindVideo PostKind = "video"
	// KindImage and KindCarousel are planned (phase 2).
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Scope tells what an issue blocks: content errors block approval,
// account errors (not connected, missing credentials) only block publishing.
type Scope string

const (
	ScopeContent Scope = "content"
	ScopeAccount Scope = "account"
)

type ValidationIssue struct {
	Platform ID       `json:"platform"`
	Severity Severity `json:"severity"`
	Scope    Scope    `json:"scope"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	AutoFix  string   `json:"autoFix,omitempty"`
}

// CaptionRules are the text limits of a platform.
type CaptionRules struct {
	MaxChars    int  `json:"maxChars"`
	CountUTF16  bool `json:"countUtf16"` // TikTok counts UTF-16 code units
	MaxHashtags int  `json:"maxHashtags,omitempty"`
	MaxMentions int  `json:"maxMentions,omitempty"`
}

// Constraints bundle everything a platform accepts for one post kind.
type Constraints struct {
	Video   *media.VideoConstraints `json:"video,omitempty"`
	Caption CaptionRules            `json:"caption"`
}

// ResolvedTarget is a post as one platform will receive it.
type ResolvedTarget struct {
	PostID   int64           `json:"postId"`
	Kind     PostKind        `json:"kind"`
	Caption  string          `json:"caption"` // final text incl. hashtags
	Options  json.RawMessage `json:"options"`
	CoverMs  int64           `json:"coverMs"`
	FilePath string          `json:"-"` // the rendition that gets uploaded
	File     media.Info      `json:"file"`
}

type TokenSet struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	AccountID        string
	AccountName      string
	Scopes           string
}

// NeedsRefresh reports whether the access token expires within the platform's margin.
func (t TokenSet) NeedsRefresh(margin time.Duration) bool {
	return !t.ExpiresAt.IsZero() && time.Until(t.ExpiresAt) < margin
}

type Authenticator interface {
	// AuthorizeURL is where the user's browser is sent to grant access. verifier is a
	// PKCE code verifier; adapters that use PKCE derive their challenge from it.
	AuthorizeURL(state, redirectURI, verifier string) string
	Exchange(ctx context.Context, code, redirectURI, verifier string) (TokenSet, error)
	Refresh(ctx context.Context, t TokenSet) (TokenSet, error)
	// RefreshMargin: refresh when the token expires sooner than this.
	RefreshMargin() time.Duration
}

// PublishContext gives an adapter what it needs during an upload.
type PublishContext struct {
	Token    TokenSet
	Log      func(format string, args ...any)
	Progress func(stage string)
}

type PublishResult struct {
	RemoteID  string `json:"remoteId"`
	Permalink string `json:"permalink,omitempty"`
	Note      string `json:"note,omitempty"` // e.g. "Sent to TikTok inbox — finish posting in the app"
}

type Adapter interface {
	ID() ID
	DisplayName() string
	SupportedKinds() []PostKind
	Constraints(kind PostKind) Constraints

	// OptionsSchema is a JSON Schema (object) for platform-specific options.
	// The UI renders a form from it; MCP exposes it to Claude.
	OptionsSchema() json.RawMessage
	DefaultOptions() json.RawMessage
	// Validate checks platform-specific options and rules.
	Validate(t ResolvedTarget) []ValidationIssue
	// ReviewNotices are texts the platform requires to be shown before publishing.
	ReviewNotices(options json.RawMessage) []string

	// Configured reports whether app credentials are present.
	Configured() bool
	Auth() Authenticator

	// Publish uploads the target and blocks until the platform reports a final state.
	Publish(ctx context.Context, pc PublishContext, t ResolvedTarget) (PublishResult, error)
}

// AccountInfoProvider is optionally implemented to show live account details
// (e.g. TikTok's creator info, which the UI must display before posting).
type AccountInfoProvider interface {
	AccountInfo(ctx context.Context, token TokenSet) (any, error)
}

// SetupReporter is optionally implemented to name the settings that are still
// missing (e.g. environment variables), so the UI can tell the user what to add.
type SetupReporter interface {
	MissingSettings() []string
}

// TokenImporter is optionally implemented by platforms that let the user paste
// an access token generated in their developer dashboard, skipping the OAuth
// redirect (which needs a public HTTPS URL).
type TokenImporter interface {
	ImportToken(ctx context.Context, accessToken string) (TokenSet, error)
}

// LocalRedirector is optionally implemented by platforms that accept an
// http://localhost OAuth redirect (e.g. TikTok desktop apps), so no tunnel is needed.
type LocalRedirector interface {
	AllowsLocalRedirect() bool
}
