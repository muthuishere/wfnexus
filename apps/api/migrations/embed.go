// Package migrations embeds the golang-migrate SQL trees so the API applies
// them on boot.
//
// Two dialects, two trees, ONE embedded filesystem: the Postgres set is this
// directory (where it has always been, and where a new Postgres migration still
// goes), the SQLite set is sqlite/. The store picks the subdirectory by storage
// driver, so each database keeps its own version line.
package migrations

import "embed"

//go:embed *.sql sqlite/*.sql
var FS embed.FS
