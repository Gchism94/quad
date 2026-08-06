// SPDX-License-Identifier: AGPL-3.0-or-later

// Package importer applies an existing course, described in host-neutral terms,
// into Cairn's store.
//
// It exists for the GitHub Classroom migration but knows nothing about GitHub: a
// Plan is plain data, so a future GitLab or Forgejo importer reuses this package
// by producing the same shape. The host-specific half of the GitHub path lives in
// pkg/adapter/github/classroom; only ghc.go in this package maps between them.
//
// Two invariants shape the design:
//
//   - It writes to the Cairn store only. Nothing here touches a Git host — the
//     repositories being imported already exist and are left exactly as they are.
//   - A Plan carries host usernames and repository names, never a legal name, SIS
//     id, or plaintext email. See internal/store/models.go.
package importer

import (
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Plan is a complete description of the course to import. Applying the same Plan
// twice is a no-op.
type Plan struct {
	// Host and Namespace identify where the repositories already live, e.g.
	// github + "INFO-523-S26".
	Host      adapter.Host
	Namespace string
	// Name is the course label shown in Cairn.
	Name string
	// JoinPolicy is store.ClassroomJoinPolicyRoster or ...Open. Empty means
	// roster, which is the right default for an import: the roster is known.
	JoinPolicy string
	// Source records where the plan came from, e.g. "github-classroom". It is
	// recorded on imported grades so they stay distinguishable from Cairn's own.
	Source string

	Assignments []PlannedAssignment
}

// PlannedAssignment is one assignment and every repository already created for
// it.
type PlannedAssignment struct {
	Title string
	// Slug is the natural key an assignment is matched on when re-importing.
	Slug string
	// Group reports a team assignment, whose repositories are shared and whose
	// names are chosen by students rather than derived from a username.
	Group bool
	// Deadline is nil when the assignment has none. A deadline already in the
	// past is imported as-is; see Options.RetroactiveLock.
	Deadline *time.Time
	// Template is the starter-code repository, when one is known.
	Template adapter.TemplateRef

	Repos []PlannedRepo
}

// PlannedRepo is one existing repository and the student(s) it belongs to.
type PlannedRepo struct {
	// Name is the repository name within the namespace, as recorded by the host.
	// It is never reconstructed from a naming convention.
	Name string
	// Members are the host usernames sharing this repository. Individual
	// assignments have exactly one; teams may have several.
	Members []string
	// Score and MaxScore are nil together when the host reported no grade.
	Score    *float64
	MaxScore *float64
	// GradeRaw is the host's original grade string (e.g. "10/90"), kept for
	// provenance on the imported grade.
	GradeRaw string
}
