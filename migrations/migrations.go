// Package migrations embeds the SQL migrations so the server binary can apply
// them itself (`server migrate`).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
