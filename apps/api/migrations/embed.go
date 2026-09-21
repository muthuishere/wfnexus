// Package migrations embeds the golang-migrate SQL tree so the API applies it on boot.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
