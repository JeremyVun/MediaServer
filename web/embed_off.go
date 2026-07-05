//go:build !embedweb

package webdist

import "io/fs"

// FS returns nil in non-embedded builds; the server serves a placeholder
// and the Vite dev server hosts the UI (proxying /api here).
func FS() fs.FS { return nil }
