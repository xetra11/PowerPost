package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ResolveInboxPath turns a path as Claude sees it on the host (e.g.
// C:\Development\power-poster\inbox\reel.mp4) into a path this process can
// open (e.g. /inbox/reel.mp4 inside Docker). Only files inside the inbox are allowed.
func (a *App) ResolveInboxPath(p string) (string, error) {
	norm := func(s string) string {
		s = strings.ReplaceAll(strings.TrimSpace(s), `\`, "/")
		return strings.TrimRight(s, "/")
	}
	in := norm(p)
	var rel string
	found := false
	for _, root := range []string{norm(a.Cfg.InboxHostPath), norm(a.Cfg.InboxDir)} {
		if root == "" {
			continue
		}
		if len(in) > len(root) && strings.EqualFold(in[:len(root)], root) && in[len(root)] == '/' {
			rel, found = in[len(root)+1:], true
			break
		}
	}
	if !found {
		return "", invalid("%q is outside the inbox folder. Save the file into %s (or use upload_media)", p, a.Cfg.InboxHostPath)
	}
	local := filepath.Join(a.Cfg.InboxDir, filepath.FromSlash(rel))
	if r, err := filepath.Rel(a.Cfg.InboxDir, local); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", invalid("%q points outside the inbox folder", p)
	}
	st, err := os.Stat(local)
	if err != nil {
		return "", invalid("file not found in inbox: %s", p)
	}
	if !st.Mode().IsRegular() {
		return "", invalid("%s is not a file", p)
	}
	return local, nil
}

// mergeOptions applies patch on top of current, checking keys and types
// against the adapter's options schema.
func mergeOptions(schemaRaw, current, patch json.RawMessage) (json.RawMessage, error) {
	var schema struct {
		Properties map[string]struct {
			Type string `json:"type"`
			Enum []any  `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, err
	}
	cur := map[string]any{}
	_ = json.Unmarshal(current, &cur)
	var p map[string]any
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, fmt.Errorf("options must be a JSON object")
	}
	for k, v := range p {
		prop, ok := schema.Properties[k]
		if !ok {
			var known []string
			for name := range schema.Properties {
				known = append(known, name)
			}
			slices.Sort(known)
			return nil, fmt.Errorf("unknown option %q (known: %s)", k, strings.Join(known, ", "))
		}
		switch prop.Type {
		case "boolean":
			if _, ok := v.(bool); !ok {
				return nil, fmt.Errorf("option %q must be true or false", k)
			}
		case "string":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("option %q must be a string", k)
			}
			if len(prop.Enum) > 0 && s != "" && !slices.Contains(prop.Enum, any(s)) {
				return nil, fmt.Errorf("option %q must be one of %v", k, prop.Enum)
			}
		}
		cur[k] = v
	}
	return json.Marshal(cur)
}

// PublicMediaURL returns a signed, expiring link to a rendition or cover image, served by the
// public listener through the tunnel. Platforms like Instagram fetch videos
// from such links. ok is false when no tunnel (PUBLIC_BASE_URL) is configured.
func (a *App) PublicMediaURL(path string, ttl time.Duration) (string, bool) {
	if a.Cfg.PublicBaseURL == "" {
		return "", false
	}
	payload := fmt.Sprintf("%s.%d", filepath.Base(path), time.Now().Add(ttl).Unix())
	return a.Cfg.PublicBaseURL + "/public-media/" + payload + "." + a.Box.Sign(payload), true
}

var verificationRe = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9_.-]*\.txt|google[0-9a-f]+\.html)$`)

// VerificationFile returns a site-verification file from the inbox's "verify"
// folder: TikTok's URL-prefix signature file (TikTok pulls photos only from
// verified URL prefixes) or Google Search Console's google<hash>.html (Google
// requires a verified home page for the OAuth consent screen). Serving the
// file at the tunnel's root verifies it. Only .txt files and google*.html in
// that folder are public, nothing else from the inbox.
func (a *App) VerificationFile(name string) (string, bool) {
	if !verificationRe.MatchString(name) {
		return "", false
	}
	p := filepath.Join(a.Cfg.InboxDir, "verify", name)
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

// ResolvePublicMedia checks a public media token and returns the file to serve.
func (a *App) ResolvePublicMedia(token string) (string, bool) {
	i := strings.LastIndex(token, ".")
	if i < 0 {
		return "", false
	}
	payload, sig := token[:i], token[i+1:]
	if !a.Box.Verify(payload, sig) {
		return "", false
	}
	j := strings.LastIndex(payload, ".")
	if j < 0 {
		return "", false
	}
	exp, err := strconv.ParseInt(payload[j+1:], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	name := payload[:j]
	if name != filepath.Base(name) || name == "" || name[0] == '.' {
		return "", false
	}
	for _, dir := range []string{a.renditionsDir(), a.coversDir()} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}
