// Package db embeds the SQL migrations so a built binary carries its own schema.
package db

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS
