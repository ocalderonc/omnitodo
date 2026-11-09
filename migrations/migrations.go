// Package migrations embeds the goose SQL migrations so the binary carries
// its own schema and migrates the database at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
