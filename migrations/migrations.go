// Package migrations embeds the goose SQL migrations so the binary carries
// its own schema and migrates the database at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

// Version 2 is a Go migration (store.migrationNameKey), so the next SQL file
// is 00003 (taken by activity_company), so next is 00004.
