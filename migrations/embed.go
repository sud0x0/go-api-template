// Package migrations embeds the SQL migration files into binaries at build time.
//
// The embedded FS is consumed by cmd/migrate (the production migrator binary)
// so a single static binary carries every schema change it needs to apply.
// New migrations must be placed in this directory and follow goose's naming
// convention (NNNNN_description.sql).
package migrations

import "embed"

// FS is the embedded migration filesystem consumed by cmd/migrate. Every
// .sql file under migrations/ is included at compile time; goose discovers
// them by reading this FS.
//
//go:embed *.sql
var FS embed.FS
