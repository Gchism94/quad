// SPDX-License-Identifier: AGPL-3.0-or-later

// Package memory is an in-memory implementation of store.Store. It backs
// hermetic tests and local development; it is not durable.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Store is a goroutine-safe in-memory store.Store.
type Store struct {
	mu          sync.RWMutex
	users       map[string]store.User
	classrooms  map[string]store.Classroom
	assignments map[string]store.Assignment
	roster      map[string]store.RosterEntry
	submissions map[string]store.Submission
	grades      map[string]store.Grade
	gradingRuns map[string]store.GradingRun
	jobs        map[string]store.ProvisioningJob
	jobIdem     map[string]bool
}

// New returns an empty in-memory store.
func New() *Store {
	return &Store{
		users:       map[string]store.User{},
		classrooms:  map[string]store.Classroom{},
		assignments: map[string]store.Assignment{},
		roster:      map[string]store.RosterEntry{},
		submissions: map[string]store.Submission{},
		grades:      map[string]store.Grade{},
		gradingRuns: map[string]store.GradingRun{},
		jobs:        map[string]store.ProvisioningJob{},
		jobIdem:     map[string]bool{},
	}
}

// Compile-time guarantee that *Store satisfies the interface.
var _ store.Store = (*Store)(nil)

// --- users ---

func (m *Store) CreateUser(_ context.Context, u *store.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[u.ID] = *u
	return nil
}

func (m *Store) GetUser(_ context.Context, id string) (*store.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &u, nil
}

func (m *Store) FindUserByHostUsername(_ context.Context, host adapter.Host, username string) (*store.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.Host == host && u.HostUsername == username {
			u := u
			return &u, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *Store) FindUserByHostUserID(_ context.Context, host adapter.Host, hostUserID string) (*store.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.Host == host && u.HostUserID == hostUserID {
			u := u
			return &u, nil
		}
	}
	return nil, store.ErrNotFound
}

// --- classrooms ---

func (m *Store) CreateClassroom(_ context.Context, c *store.Classroom) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.classrooms[c.ID] = *c
	return nil
}

func (m *Store) GetClassroom(_ context.Context, id string) (*store.Classroom, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.classrooms[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &c, nil
}

func (m *Store) ListClassrooms(_ context.Context) ([]*store.Classroom, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*store.Classroom
	for _, c := range m.classrooms {
		c := c
		out = append(out, &c)
	}
	return out, nil
}

// --- assignments ---

func (m *Store) CreateAssignment(_ context.Context, a *store.Assignment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignments[a.ID] = *a
	return nil
}

func (m *Store) GetAssignment(_ context.Context, id string) (*store.Assignment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.assignments[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &a, nil
}

func (m *Store) ListAssignmentsByClassroom(_ context.Context, classroomID string) ([]*store.Assignment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*store.Assignment
	for _, a := range m.assignments {
		if a.ClassroomID == classroomID {
			a := a
			out = append(out, &a)
		}
	}
	return out, nil
}

func (m *Store) UpdateAssignment(_ context.Context, a *store.Assignment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.assignments[a.ID]; !ok {
		return store.ErrNotFound
	}
	m.assignments[a.ID] = *a
	return nil
}

func (m *Store) ListAssignmentsDueBy(_ context.Context, t time.Time) ([]*store.Assignment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*store.Assignment
	for _, a := range m.assignments {
		if a.Deadline != nil && !a.Deadline.After(t) {
			a := a
			out = append(out, &a)
		}
	}
	return out, nil
}

// --- roster ---

func (m *Store) CreateRosterEntry(_ context.Context, r *store.RosterEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.roster[r.ID] = *r
	return nil
}

func (m *Store) GetRosterEntry(_ context.Context, id string) (*store.RosterEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.roster[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &r, nil
}

func (m *Store) FindRosterEntryByUsername(_ context.Context, classroomID, username string) (*store.RosterEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.roster {
		if r.ClassroomID == classroomID && r.HostUsername == username {
			r := r
			return &r, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *Store) UpdateRosterEntry(_ context.Context, r *store.RosterEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.roster[r.ID]; !ok {
		return store.ErrNotFound
	}
	m.roster[r.ID] = *r
	return nil
}

func (m *Store) ListRosterEntries(_ context.Context, classroomID string) ([]*store.RosterEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*store.RosterEntry
	for _, r := range m.roster {
		if r.ClassroomID == classroomID {
			r := r
			out = append(out, &r)
		}
	}
	return out, nil
}

// --- submissions ---

func (m *Store) CreateSubmission(_ context.Context, s *store.Submission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ex := range m.submissions {
		if ex.AssignmentID == s.AssignmentID && ex.RosterEntryID == s.RosterEntryID {
			return store.ErrConflict
		}
	}
	m.submissions[s.ID] = *s
	return nil
}

func (m *Store) GetSubmission(_ context.Context, id string) (*store.Submission, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.submissions[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &s, nil
}

func (m *Store) FindSubmission(_ context.Context, assignmentID, rosterEntryID string) (*store.Submission, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.submissions {
		if s.AssignmentID == assignmentID && s.RosterEntryID == rosterEntryID {
			s := s
			return &s, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *Store) FindSubmissionByRepo(_ context.Context, host adapter.Host, namespace, name string) (*store.Submission, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.submissions {
		if s.Repo.Host == host && s.Repo.Namespace == namespace && s.Repo.Name == name {
			s := s
			return &s, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *Store) ListSubmissionsByRosterUsername(_ context.Context, host adapter.Host, username string) ([]*store.Submission, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// The roster entries owned by this (host, username) identify the student's
	// submissions. Only the Git username is matched — no name/email/SIS involved.
	rosterIDs := map[string]bool{}
	for _, r := range m.roster {
		if r.Host == host && r.HostUsername == username {
			rosterIDs[r.ID] = true
		}
	}
	var out []*store.Submission
	for _, s := range m.submissions {
		if rosterIDs[s.RosterEntryID] {
			s := s
			out = append(out, &s)
		}
	}
	sortSubmissionsByActivityDesc(out)
	return out, nil
}

// sortSubmissionsByActivityDesc orders newest-activity-first; a nil LastActivityAt
// sorts last. Ties break by ID for a stable order.
func sortSubmissionsByActivityDesc(subs []*store.Submission) {
	sort.SliceStable(subs, func(i, j int) bool {
		a, b := subs[i].LastActivityAt, subs[j].LastActivityAt
		switch {
		case a == nil && b == nil:
			return subs[i].ID < subs[j].ID
		case a == nil:
			return false
		case b == nil:
			return true
		case a.Equal(*b):
			return subs[i].ID < subs[j].ID
		default:
			return a.After(*b)
		}
	})
}

func (m *Store) UpdateSubmission(_ context.Context, s *store.Submission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.submissions[s.ID]; !ok {
		return store.ErrNotFound
	}
	m.submissions[s.ID] = *s
	return nil
}

func (m *Store) ListSubmissionsByAssignment(_ context.Context, assignmentID string) ([]*store.Submission, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*store.Submission
	for _, s := range m.submissions {
		if s.AssignmentID == assignmentID {
			s := s
			out = append(out, &s)
		}
	}
	return out, nil
}

func (m *Store) ListSubmissionsByClassroom(ctx context.Context, classroomID string) ([]*store.Submission, error) {
	assignments, _ := m.ListAssignmentsByClassroom(ctx, classroomID)
	inClassroom := map[string]bool{}
	for _, a := range assignments {
		inClassroom[a.ID] = true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*store.Submission
	for _, s := range m.submissions {
		if inClassroom[s.AssignmentID] {
			s := s
			out = append(out, &s)
		}
	}
	return out, nil
}

// --- grades ---

func (m *Store) CreateGrade(_ context.Context, g *store.Grade) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grades[g.ID] = *g
	return nil
}

func (m *Store) LatestGradeForSubmission(_ context.Context, submissionID string) (*store.Grade, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest *store.Grade
	for _, g := range m.grades {
		if g.SubmissionID != submissionID {
			continue
		}
		if latest == nil || g.GradedAt.After(latest.GradedAt) {
			g := g
			latest = &g
		}
	}
	if latest == nil {
		return nil, store.ErrNotFound
	}
	return latest, nil
}

func (m *Store) ListGradesBySubmission(_ context.Context, submissionID string) ([]*store.Grade, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*store.Grade
	for _, g := range m.grades {
		if g.SubmissionID == submissionID {
			g := g
			out = append(out, &g)
		}
	}
	// Most recent first; ties break by ID for a stable order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].GradedAt.Equal(out[j].GradedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].GradedAt.After(out[j].GradedAt)
	})
	return out, nil
}

// --- grading runs ---

func (m *Store) CreateGradingRun(_ context.Context, run *store.GradingRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gradingRuns[run.ID] = *run
	return nil
}

func (m *Store) UpdateGradingRun(_ context.Context, run *store.GradingRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.gradingRuns[run.ID]; !ok {
		return store.ErrNotFound
	}
	m.gradingRuns[run.ID] = *run
	return nil
}

func (m *Store) ListGradingRunsBySubmission(_ context.Context, submissionID string) ([]*store.GradingRun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*store.GradingRun
	for _, run := range m.gradingRuns {
		if run.SubmissionID == submissionID {
			run := run
			out = append(out, &run)
		}
	}
	return out, nil
}

// --- provisioning jobs ---

func (m *Store) CreateJob(_ context.Context, j *store.ProvisioningJob) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j.IdempotencyKey != "" && m.jobIdem[j.IdempotencyKey] {
		return false, nil
	}
	m.jobs[j.ID] = *j
	if j.IdempotencyKey != "" {
		m.jobIdem[j.IdempotencyKey] = true
	}
	return true, nil
}

func (m *Store) ClaimNextJob(_ context.Context) (*store.ProvisioningJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var pick *store.ProvisioningJob
	for id, j := range m.jobs {
		if j.Status != store.JobPending || j.ScheduledAt.After(now) {
			continue
		}
		if pick == nil || j.ScheduledAt.Before(pick.ScheduledAt) {
			j := j
			j.ID = id
			pick = &j
		}
	}
	if pick == nil {
		return nil, store.ErrNotFound
	}
	pick.Status = store.JobInProgress
	m.jobs[pick.ID] = *pick
	return pick, nil
}

func (m *Store) UpdateJob(_ context.Context, j *store.ProvisioningJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; !ok {
		return store.ErrNotFound
	}
	m.jobs[j.ID] = *j
	return nil
}
