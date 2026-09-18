// Package migrations carries the SQL schema of the service inside the binary.
package migrations

import "embed"

// Files holds NNNN_description.sql files; internal/db applies them in order.
//
//go:embed *.sql
var Files embed.FS
