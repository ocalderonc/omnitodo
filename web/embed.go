// Package web embeds the HTML templates and static assets into the binary.
package web

import "embed"

//go:embed templates/*.html
var Templates embed.FS

//go:embed static
var Static embed.FS
