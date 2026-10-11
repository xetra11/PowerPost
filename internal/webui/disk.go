//go:build !embedui

package webui

import "io/fs"

// FS returns nil without the "embedui" tag; the server then serves the UI
// from disk (PP_WEB_DIR) if it was built, or shows a hint page.
func FS() fs.FS { return nil }
