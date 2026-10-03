// Package migrations embeds the SQL migrations, one directory per module
// (Technical Doc §7.5): db/migrations/<module>/NNNNN_<name>.sql.
package migrations

import "embed"

//go:embed */*.sql
var FS embed.FS

// Order is the module apply order (dependencies first). Every module keeps
// its own goose version table, public.goose_<module>.
var Order = []string{
	"platform",
	"audit",
	"golf",
	"billing",
	"commercial",
	"reservation",
	"crm",
	"membership",
	"sportclub",
	"procurement",
	"reporting",
}
