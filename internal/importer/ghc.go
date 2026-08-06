// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"errors"
	"fmt"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter/github/classroom"
)

// SourceGitHubClassroom labels grades imported from GitHub Classroom.
const SourceGitHubClassroom = "github-classroom"

// FromGitHubClassroom converts a captured GitHub Classroom snapshot into a
// host-neutral Plan. It is the only place in this package that knows what GitHub
// Classroom is; everything downstream sees plain data.
//
// orgOverride replaces the organization recorded in the snapshot when non-empty.
//
// Anything unusable is reported in warnings and left out of the plan rather than
// guessed at — a repository whose owner is not the classroom's org, an assignment
// with no slug, an acceptance with no repository or no students. Callers should
// surface warnings; they are the difference between "nothing to import" and
// "quietly imported less than you have".
func FromGitHubClassroom(snap *classroom.Snapshot, orgOverride string) (Plan, []string, error) {
	if snap == nil {
		return Plan{}, nil, errors.New("importer: nil snapshot")
	}
	org := orgOverride
	if org == "" {
		org = snap.OrgLogin()
	}
	if org == "" {
		return Plan{}, nil, errors.New("importer: snapshot has no organization — re-capture it, or pass the org explicitly")
	}

	var warnings []string
	warn := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	p := Plan{
		Host:      adapter.HostGitHub,
		Namespace: org,
		Name:      snap.Classroom.Name,
		Source:    SourceGitHubClassroom,
	}

	for _, rec := range snap.Assignments {
		as := rec.Assignment
		if as.Slug == "" {
			warn("assignment %d (%q) has no slug — skipped", as.ID, as.Title)
			continue
		}
		pa := PlannedAssignment{
			Title:    as.Title,
			Slug:     as.Slug,
			Group:    as.IsGroup(),
			Deadline: as.Deadline,
			Template: templateRef(as.StarterCodeRepository),
		}
		if pa.Title == "" {
			pa.Title = as.Slug
		}

		for _, acc := range rec.Accepted {
			owner, name := classroom.SplitFullName(acc.Repository.FullName)
			if name == "" {
				name = acc.Repository.Name // fall back to the bare name
			}
			if name == "" {
				warn("assignment %s: an acceptance has no repository — skipped", as.Slug)
				continue
			}
			// A Plan has one namespace, so a repo living somewhere else cannot be
			// addressed correctly. Report it instead of writing a wrong RepoRef.
			if owner != "" && owner != org {
				warn("assignment %s: repository %s is not in org %s — skipped", as.Slug, acc.Repository.FullName, org)
				continue
			}

			members := make([]string, 0, len(acc.Students))
			for _, s := range acc.Students {
				if s.Login != "" {
					members = append(members, s.Login)
				}
			}
			if len(members) == 0 {
				warn("assignment %s: repository %s has no student accounts — skipped", as.Slug, name)
				continue
			}

			pr := PlannedRepo{Name: name, Members: members, GradeRaw: acc.Grade}
			if awarded, available, ok := classroom.ParseGrade(acc.Grade); ok {
				pr.Score, pr.MaxScore = &awarded, &available
			} else if acc.Grade != "" {
				warn("assignment %s: repository %s has an unparseable grade %q — imported without a score", as.Slug, name, acc.Grade)
			}
			pa.Repos = append(pa.Repos, pr)
		}
		p.Assignments = append(p.Assignments, pa)
	}
	return p, warnings, nil
}

// templateRef maps a Classroom starter-code repository onto Cairn's template
// reference. A missing starter repo yields a zero TemplateRef, which is valid: an
// imported assignment is never re-generated from its template.
func templateRef(repo *classroom.Repository) adapter.TemplateRef {
	if repo == nil {
		return adapter.TemplateRef{}
	}
	owner, name := classroom.SplitFullName(repo.FullName)
	if name == "" {
		name = repo.Name
	}
	if name == "" {
		return adapter.TemplateRef{}
	}
	return adapter.TemplateRef{
		Host:      adapter.HostGitHub,
		Namespace: owner,
		Name:      name,
		Ref:       repo.DefaultBranch,
	}
}
