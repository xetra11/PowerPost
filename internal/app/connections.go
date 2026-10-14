package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"powerposter/internal/media"
	"powerposter/internal/platform"
	"powerposter/internal/store"
)

type PlatformView struct {
	ID              string                  `json:"id"`
	DisplayName     string                  `json:"displayName"`
	Configured      bool                    `json:"configured"`
	Connected       bool                    `json:"connected"`
	AccountName     string                  `json:"accountName"`
	ExpiresAt       *time.Time              `json:"expiresAt"`
	RedirectURI     string                  `json:"redirectUri"`
	MissingSettings []string                `json:"missingSettings"` // .env entries needed before Connect works
	CanImportToken  bool                    `json:"canImportToken"`  // a dashboard-generated token can be pasted instead
	TokenPrompt     *platform.TokenPrompt   `json:"tokenPrompt"`     // what to paste (with CanImportToken)
	SupportedKinds  []platform.PostKind     `json:"supportedKinds"`
	Constraints     platform.Constraints    `json:"constraints"`      // for video posts
	ImageLimits     *media.ImageConstraints `json:"imageConstraints"` // for image posts (nil: not supported)
	OptionsSchema   any                     `json:"optionsSchema"`
	DefaultOptions  any                     `json:"defaultOptions"`
}

func (a *App) Platforms(ctx context.Context, projectID int64) ([]PlatformView, error) {
	if _, err := a.project(ctx, projectID); err != nil {
		return nil, err
	}
	out := []PlatformView{}
	for _, ad := range a.Registry.All() {
		pv := PlatformView{
			ID:              string(ad.ID()),
			DisplayName:     ad.DisplayName(),
			Configured:      ad.Configured(),
			RedirectURI:     a.redirectURI(ad),
			MissingSettings: a.missingSettings(ad),
			CanImportToken:  isImporter(ad),
			TokenPrompt:     tokenPrompt(ad),
			SupportedKinds:  ad.SupportedKinds(),
			Constraints:     ad.Constraints(platform.KindVideo),
			ImageLimits:     ad.Constraints(platform.KindImage).Image,
			OptionsSchema:   ad.OptionsSchema(),
			DefaultOptions:  ad.DefaultOptions(),
		}
		conn, err := a.Store.GetConnection(ctx, projectID, string(ad.ID()))
		if err == nil {
			pv.Connected = true
			pv.AccountName = conn.AccountName
			pv.ExpiresAt = conn.ExpiresAt
			if conn.RefreshExpiresAt != nil {
				pv.ExpiresAt = conn.RefreshExpiresAt // what matters to the user: when to reconnect
			} else if len(conn.RefreshTokenEnc) > 0 {
				pv.ExpiresAt = nil // refresh token without expiry (Google): no need to reconnect
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		out = append(out, pv)
	}
	return out, nil
}

// redirectURI is where the platform sends the browser after sign-in: the public
// tunnel URL if configured, otherwise http://localhost for platforms that allow it.
func (a *App) redirectURI(ad platform.Adapter) string {
	if tokenOnly(ad) {
		return ""
	}
	path := "/oauth/" + string(ad.ID()) + "/callback"
	if a.Cfg.PublicBaseURL != "" {
		return a.Cfg.PublicBaseURL + path
	}
	if allowsLocalRedirect(ad) {
		return a.localBaseURL() + path
	}
	return ""
}

// localBaseURL is the private listener as http://localhost:<port> (platforms
// accept "localhost" but often not "127.0.0.1").
func (a *App) localBaseURL() string {
	u, err := url.Parse(a.Cfg.BaseURL)
	if err != nil {
		return a.Cfg.BaseURL
	}
	if h, port := u.Hostname(), u.Port(); h == "127.0.0.1" || h == "::1" {
		u.Host = "localhost"
		if port != "" {
			u.Host += ":" + port
		}
	}
	return strings.TrimRight(u.String(), "/")
}

func allowsLocalRedirect(ad platform.Adapter) bool {
	l, ok := ad.(platform.LocalRedirector)
	return ok && l.AllowsLocalRedirect()
}

// StartConnect returns the platform's consent URL for the user's browser.
func (a *App) StartConnect(ctx context.Context, projectID int64, id string) (string, error) {
	if _, err := a.project(ctx, projectID); err != nil {
		return "", err
	}
	ad, ok := a.Registry.Get(platform.ID(id))
	if !ok {
		return "", notFound("unknown platform %q", id)
	}
	if tokenOnly(ad) {
		return "", invalid("%s has no sign-in; paste the %s in Settings instead", ad.DisplayName(), lowerFirst(tokenPrompt(ad).Label))
	}
	if !ad.Configured() {
		return "", invalid("%s app credentials are missing. Add them to .env and restart — see README → Connecting platforms", ad.DisplayName())
	}
	redirect := a.redirectURI(ad)
	if redirect == "" {
		return "", invalid("PUBLIC_BASE_URL is not set. %s needs an HTTPS callback URL — see README → Tunnel", ad.DisplayName())
	}
	state, verifier := randomHex(16), randomHex(32) // 64 hex chars: a valid PKCE verifier
	if err := a.Store.SaveOAuthState(ctx, state, store.OAuthState{ProjectID: projectID, Platform: id, Verifier: verifier}); err != nil {
		return "", err
	}
	return ad.Auth().AuthorizeURL(state, redirect, verifier), nil
}

// FinishConnect handles the OAuth callback and returns the project the
// account was connected to (0 if the state was unknown).
func (a *App) FinishConnect(ctx context.Context, id, code, state string) (int64, error) {
	ad, ok := a.Registry.Get(platform.ID(id))
	if !ok {
		return 0, notFound("unknown platform %q", id)
	}
	saved, err := a.Store.TakeOAuthState(ctx, state, 15*time.Minute)
	if err != nil || saved.Platform != id {
		return 0, invalid("the sign-in link expired or was already used; start again from Settings")
	}
	if _, err := a.project(ctx, saved.ProjectID); err != nil {
		return 0, err
	}
	ts, err := ad.Auth().Exchange(ctx, code, a.redirectURI(ad), saved.Verifier)
	if err != nil {
		return saved.ProjectID, fmt.Errorf("%s sign-in failed: %w", ad.DisplayName(), err)
	}
	return saved.ProjectID, a.saveToken(ctx, saved.ProjectID, id, ts)
}

// ImportToken connects a platform with an access token the user generated in
// the platform's developer dashboard (no OAuth redirect or public URL needed).
func (a *App) ImportToken(ctx context.Context, projectID int64, id, accessToken string) error {
	if _, err := a.project(ctx, projectID); err != nil {
		return err
	}
	ad, ok := a.Registry.Get(platform.ID(id))
	if !ok {
		return notFound("unknown platform %q", id)
	}
	imp, ok := ad.(platform.TokenImporter)
	if !ok {
		return invalid("%s does not support pasting an access token; use Connect", ad.DisplayName())
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return invalid("access token is empty")
	}
	ts, err := imp.ImportToken(ctx, accessToken)
	if err != nil {
		return invalid("%v", err)
	}
	return a.saveToken(ctx, projectID, id, ts)
}

func (a *App) Disconnect(ctx context.Context, projectID int64, id string) error {
	return a.Store.DeleteConnection(ctx, projectID, id)
}

// AccountInfo returns live account details for platforms that provide them.
func (a *App) AccountInfo(ctx context.Context, projectID int64, id string) (any, error) {
	ad, ok := a.Registry.Get(platform.ID(id))
	if !ok {
		return nil, notFound("unknown platform %q", id)
	}
	p, ok := ad.(platform.AccountInfoProvider)
	if !ok {
		return nil, nil
	}
	token, err := a.token(ctx, projectID, ad)
	if err != nil {
		return nil, err
	}
	return p.AccountInfo(ctx, token)
}

// token returns a valid access token, refreshing it when it is about to expire.
func (a *App) token(ctx context.Context, projectID int64, ad platform.Adapter) (platform.TokenSet, error) {
	conn, err := a.Store.GetConnection(ctx, projectID, string(ad.ID()))
	if errors.Is(err, store.ErrNotFound) {
		return platform.TokenSet{}, invalid("%s is not connected; connect it in Settings", ad.DisplayName())
	}
	if err != nil {
		return platform.TokenSet{}, err
	}
	ts, err := a.decodeToken(conn)
	if err != nil {
		return ts, err
	}
	if ts.NeedsRefresh(ad.Auth().RefreshMargin()) {
		fresh, err := ad.Auth().Refresh(ctx, ts)
		if err != nil {
			if time.Now().Before(ts.ExpiresAt) {
				a.Log.Warn("token refresh failed, using current token", "platform", ad.ID(), "err", err)
				return ts, nil
			}
			return ts, fmt.Errorf("%s token expired and refresh failed (%v); reconnect in Settings", ad.DisplayName(), err)
		}
		if err := a.saveToken(ctx, projectID, string(ad.ID()), fresh); err != nil {
			return fresh, err
		}
		ts = fresh
	}
	return ts, nil
}

func (a *App) decodeToken(c store.Connection) (platform.TokenSet, error) {
	access, err := a.Box.Unseal(c.AccessTokenEnc)
	if err != nil {
		return platform.TokenSet{}, err
	}
	refresh, err := a.Box.Unseal(c.RefreshTokenEnc)
	if err != nil {
		return platform.TokenSet{}, err
	}
	ts := platform.TokenSet{AccessToken: access, RefreshToken: refresh, AccountID: c.AccountID, AccountName: c.AccountName, Scopes: c.Scopes}
	if c.ExpiresAt != nil {
		ts.ExpiresAt = *c.ExpiresAt
	}
	if c.RefreshExpiresAt != nil {
		ts.RefreshExpiresAt = *c.RefreshExpiresAt
	}
	return ts, nil
}

func (a *App) saveToken(ctx context.Context, projectID int64, id string, ts platform.TokenSet) error {
	c := store.Connection{
		ProjectID:       projectID,
		Platform:        id,
		AccountID:       ts.AccountID,
		AccountName:     ts.AccountName,
		AccessTokenEnc:  a.Box.Seal(ts.AccessToken),
		RefreshTokenEnc: a.Box.Seal(ts.RefreshToken),
		Scopes:          ts.Scopes,
	}
	if !ts.ExpiresAt.IsZero() {
		c.ExpiresAt = &ts.ExpiresAt
	}
	if !ts.RefreshExpiresAt.IsZero() {
		c.RefreshExpiresAt = &ts.RefreshExpiresAt
	}
	return a.Store.SaveConnection(ctx, c)
}

// refreshLoop keeps tokens fresh while the app runs (Instagram tokens die after 60 days unused).
func (a *App) refreshLoop(ctx context.Context) {
	tick := time.NewTicker(6 * time.Hour)
	defer tick.Stop()
	for {
		conns, err := a.Store.ListConnections(ctx)
		if err != nil {
			a.Log.Warn("token refresh", "err", err)
		}
		for _, c := range conns {
			ad, ok := a.Registry.Get(platform.ID(c.Platform))
			if !ok {
				continue
			}
			if _, err := a.token(ctx, c.ProjectID, ad); err != nil {
				a.Log.Warn("token refresh", "project", c.ProjectID, "platform", ad.ID(), "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// missingSettings lists what must be added to .env before the platform can be connected.
func (a *App) missingSettings(ad platform.Adapter) []string {
	m := []string{}
	if r, ok := ad.(platform.SetupReporter); ok {
		m = append(m, r.MissingSettings()...)
	} else if !ad.Configured() {
		m = append(m, "app credentials")
	}
	if a.redirectURI(ad) == "" && !tokenOnly(ad) {
		m = append(m, "PUBLIC_BASE_URL")
	}
	return m
}

func isImporter(ad platform.Adapter) bool {
	_, ok := ad.(platform.TokenImporter)
	return ok
}

func tokenPrompt(ad platform.Adapter) *platform.TokenPrompt {
	if !isImporter(ad) {
		return nil
	}
	if p, ok := ad.(platform.TokenPrompter); ok {
		tp := p.TokenPrompt()
		return &tp
	}
	return &platform.TokenPrompt{Label: "Access token"}
}

// tokenOnly reports whether the platform connects only by pasting a secret (no OAuth).
func tokenOnly(ad platform.Adapter) bool {
	tp := tokenPrompt(ad)
	return tp != nil && tp.Only
}

// lowerFirst turns a label into a noun inside a sentence ("Webhook URL" → "webhook URL").
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
