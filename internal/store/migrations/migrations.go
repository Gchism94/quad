// SPDX-License-Identifier: AGPL-3.0-or-later

// Package migrations holds Cairn's SQL schema migrations and embeds them so the
// Postgres store can apply them without shipping loose files.
//
// There is no schema_migrations tracking table: postgres.Store.Migrate applies
// every embedded *.up.sql file, in filename order, on every startup. That only
// works because of two conventions every migration in this package must keep:
//
//   - Every new migration file uses a zero-padded four-digit prefix
//     (0005_..., never 5_...), because sorting is lexicographic on filename and
//     that is the only thing that keeps 0002 running before 0010.
//   - Every statement in a .up.sql file is idempotent — CREATE ... IF NOT
//     EXISTS, ADD COLUMN IF NOT EXISTS, CREATE INDEX IF NOT EXISTS — because
//     re-applying the whole set on every startup is exactly what happens.
package migrations

import "embed"

// FS contains the .sql migration files.
//
//go:embed *.sql
var FS embed.FS
