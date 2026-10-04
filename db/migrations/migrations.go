// Package migrations embeds the SQL migrations, one directory per module
// (Technical Doc §7.5): db/migrations/<module>/NNNNN_<name>.sql.
package migrations

import "embed"

//go:embed */*.sql
var FS embed.FS

// Order is the module apply order (dependencies first, following the layers
// of Technical Doc §4.1 from the bottom up). Every module keeps its own goose
// version table, public.goose_<module>, so reordering never re-applies a
// migration; it only matters for foreign keys between schemas on a fresh
// database.
var Order = []string{
	"platform",
	"audit",
	"crm",
	"billing",
	"commercial",
	"inventory",
	"membership",
	"reservation",
	"golf",
	"sportclub",
	"stay",
	"procurement",
	"reporting",
}
