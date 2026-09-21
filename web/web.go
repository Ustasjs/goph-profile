// Package web embeds the static web UI, so the server binary
// carries its pages and does not depend on the working directory.
package web

import "embed"

// FS holds the embedded pages.
//
//go:embed static/*.html
var FS embed.FS

// Upload and Gallery are the embedded page paths.
const (
	Upload  = "static/index.html"
	Gallery = "static/gallery.html"
)
