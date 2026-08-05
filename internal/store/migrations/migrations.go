// SPDX-License-Identifier: AGPL-3.0-or-later

// Package migrations holds Cairn's SQL schema migrations and embeds them so the
// Postgres store can apply them without shipping loose files.
package migrations

import "embed"

// FS contains the .sql migration files.
//
//go:embed *.sql
var FS embed.FS
