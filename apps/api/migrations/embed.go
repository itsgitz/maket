// Package migrations holds maket's SQL schema migrations, embedded in the
// binary. Files are named NNNN_description.sql and applied in numeric order
// by db.Migrate.
package migrations

import "embed"

// FS contains every *.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
