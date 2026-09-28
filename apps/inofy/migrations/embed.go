// Package migrations exposes the embedded App DDL so storage can apply
// it without the SQL living inside core library init (§11.2).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
