// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/grading"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
)

// gradeView is a student-facing grade summary. No PII — scores and a timestamp.
type gradeView struct {
	Score    float64   `json:"score"`
	MaxScore float64   `json:"max_score"`
	GradedAt time.Time `json:"graded_at"`
}

// testView is one per-test result from a grading breakdown.
type testView struct {
	Name      string  `json:"name"`
	Passed    bool    `json:"passed"`
	Points    float64 `json:"points"`
	MaxPoints float64 `json:"max_points"`
	Detail    string  `json:"detail,omitempty"`
}

// workItem is one submission as the student sees it. It carries only the student's
// own coursework — no other student's data, and no names/emails/SIS ids.
type workItem struct {
	SubmissionID    string     `json:"submission_id"`
	AssignmentTitle string     `json:"assignment_title"`
	AssignmentSlug  string     `json:"assignment_slug"`
	ClassroomName   string     `json:"classroom_name"`
	RepoWebURL      string     `json:"repo_web_url,omitempty"`
	Deadline        *time.Time `json:"deadline,omitempty"`
	Status          string     `json:"status"`                   // submission lifecycle
	GradingStatus   string     `json:"grading_status,omitempty"` // latest run: running/completed/failed
	LatestGrade     *gradeView `json:"latest_grade,omitempty"`
}

// workDetail adds the per-test breakdown and attempt history to a workItem.
type workDetail struct {
	workItem
	Tests   []testView  `json:"tests"`
	History []gradeView `json:"history"`
}

// handleMyWork lists the caller's own submissions. Requires a session; never
// exposes another student's work (the query is scoped to the caller's identity).
func (s *Server) handleMyWork(w http.ResponseWriter, r *http.Request) {
	host, username, ok := s.currentIdentity(r)
	if !ok {
		httpError(w, http.StatusUnauthorized, "sign in to view your work")
		return
	}
	subs, err := s.store.ListSubmissionsByRosterUsername(r.Context(), host, username)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	enr := newEnricher(s)
	items := make([]workItem, 0, len(subs))
	for _, sub := range subs {
		items = append(items, enr.item(r.Context(), sub))
	}
	writeJSON(w, http.StatusOK, map[string]any{"work": items})
}

// handleMyWorkDetail returns one submission owned by the caller, with the per-test
// breakdown and attempt history. A submission the caller does not own returns 404
// — never revealing that another student's submission exists.
func (s *Server) handleMyWorkDetail(w http.ResponseWriter, r *http.Request) {
	host, username, ok := s.currentIdentity(r)
	if !ok {
		httpError(w, http.StatusUnauthorized, "sign in to view your work")
		return
	}
	subID := r.PathValue("submissionID")
	sub, err := s.store.GetSubmission(r.Context(), subID)
	if err != nil {
		// Unknown id and not-owned are indistinguishable to the caller.
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	// Authorization: the submission's roster entry must match the caller's identity.
	re, err := s.store.GetRosterEntry(r.Context(), sub.RosterEntryID)
	if err != nil || re.Host != host || re.HostUsername != username {
		httpError(w, http.StatusNotFound, "not found")
		return
	}

	enr := newEnricher(s)
	detail := workDetail{workItem: enr.item(r.Context(), sub)}

	// Per-test breakdown from the most recent grade's stored Result JSON.
	if latest, err := s.store.LatestGradeForSubmission(r.Context(), subID); err == nil {
		var res grading.Result
		if json.Unmarshal(latest.Breakdown, &res) == nil {
			for _, t := range res.Tests {
				detail.Tests = append(detail.Tests, testView{
					Name: t.Name, Passed: t.Passed, Points: t.Points, MaxPoints: t.MaxPoints, Detail: t.Detail,
				})
			}
		}
	}

	// Attempt history (most recent first).
	if grades, err := s.store.ListGradesBySubmission(r.Context(), subID); err == nil {
		for _, g := range grades {
			detail.History = append(detail.History, gradeView{Score: g.Score, MaxScore: g.MaxScore, GradedAt: g.GradedAt})
		}
	}

	writeJSON(w, http.StatusOK, detail)
}

// enricher builds workItems, caching assignment/classroom lookups so a student's
// list of submissions doesn't refetch the same parents repeatedly.
type enricher struct {
	s           *Server
	classrooms  map[string]*store.Classroom
	assignments map[string]*store.Assignment
}

func newEnricher(s *Server) *enricher {
	return &enricher{s: s, classrooms: map[string]*store.Classroom{}, assignments: map[string]*store.Assignment{}}
}

func (e *enricher) assignment(ctx context.Context, id string) *store.Assignment {
	if a, ok := e.assignments[id]; ok {
		return a
	}
	a, err := e.s.store.GetAssignment(ctx, id)
	if err != nil {
		a = nil
	}
	e.assignments[id] = a
	return a
}

func (e *enricher) classroom(ctx context.Context, id string) *store.Classroom {
	if c, ok := e.classrooms[id]; ok {
		return c
	}
	c, err := e.s.store.GetClassroom(ctx, id)
	if err != nil {
		c = nil
	}
	e.classrooms[id] = c
	return c
}

func (e *enricher) item(ctx context.Context, sub *store.Submission) workItem {
	it := workItem{SubmissionID: sub.ID, Status: sub.Status}

	if a := e.assignment(ctx, sub.AssignmentID); a != nil {
		it.AssignmentTitle = a.Title
		it.AssignmentSlug = a.Slug
		it.Deadline = a.Deadline
		if c := e.classroom(ctx, a.ClassroomID); c != nil {
			it.ClassroomName = c.Name
		}
	}

	// Clickable repo link, when provisioned and the host adapter is configured.
	if sub.Repo.Name != "" {
		if ad := e.s.adapters[sub.Repo.Host]; ad != nil {
			it.RepoWebURL = ad.RepoWebURL(sub.Repo)
		}
	}

	if g, err := e.s.store.LatestGradeForSubmission(ctx, sub.ID); err == nil {
		it.LatestGrade = &gradeView{Score: g.Score, MaxScore: g.MaxScore, GradedAt: g.GradedAt}
	} else if !errors.Is(err, store.ErrNotFound) {
		// A real error is non-fatal for the list view; leave the grade unset.
		_ = err
	}

	it.GradingStatus = e.latestRunStatus(ctx, sub.ID)
	return it
}

// latestRunStatus returns the status of the most recently started grading run for
// a submission (empty when there are none), so the UI can show running/failed.
func (e *enricher) latestRunStatus(ctx context.Context, submissionID string) string {
	runs, err := e.s.store.ListGradingRunsBySubmission(ctx, submissionID)
	if err != nil || len(runs) == 0 {
		return ""
	}
	latest := runs[0]
	for _, run := range runs[1:] {
		if startedAfter(run.StartedAt, latest.StartedAt) {
			latest = run
		}
	}
	return latest.Status
}

// startedAfter reports whether a started strictly after b; a nil time is treated
// as the zero time (oldest).
func startedAfter(a, b *time.Time) bool {
	var at, bt time.Time
	if a != nil {
		at = *a
	}
	if b != nil {
		bt = *b
	}
	return at.After(bt)
}
