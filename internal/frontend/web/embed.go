package web

import "embed"

// staticFiles embeds the web/dist directory.
// In Go 1.18+, we need to use a relative path without .. for embed.
// We'll copy files to a local directory during build.
//
//go:embed dist
var staticFiles embed.FS
