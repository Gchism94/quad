// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when an insert violates a uniqueness constraint (e.g.
// a duplicate submission for the same assignment + roster entry).
var ErrConflict = errors.New("store: conflict")

// Store is Cairn's persistence interface. The in-memory implementation in
// ./memory backs hermetic tests and local runs; a Postgres implementation
// (database/sql) is the thin production layer added on top of the same interface.
//
// The privacy invariant lives in the data, not here: no method accepts or returns
// a student's legal name, SIS ID, or plaintext email. See DESIGN.md sections 5–6.
type Store interface {
	// Users (platform operators).
	CreateUser(ctx context.Context, u *User) error
	GetUser(ctx context.Context, id string) (*User, error)
	// FindUserByHostUsername returns the operator with the given host + username,
	// or ErrNotFound. Used to upsert an operator on login.
	FindUserByHostUsername(ctx context.Context, host adapter.Host, username string) (*User, error)
	// FindUserByHostUserID returns the operator with the given host + stable numeric
	// host user id (e.g. GitHub's integer id), or ErrNotFound. Keying on the numeric
	// id means a renamed operator reuses the same row instead of creating a new one.
	FindUserByHostUserID(ctx context.Context, host adapter.Host, hostUserID string) (*User, error)

	// Classrooms.
	CreateClassroom(ctx context.Context, c *Classroom) error
	GetClassroom(ctx context.Context, id string) (*Classroom, error)
	ListClassrooms(ctx context.Context) ([]*Classroom, error)

	// Assignments.
	CreateAssignment(ctx context.Context, a *Assignment) error
	GetAssignment(ctx context.Context, id string) (*Assignment, error)
	UpdateAssignment(ctx context.Context, a *Assignment) error
	ListAssignmentsByClassroom(ctx context.Context, classroomID string) ([]*Assignment, error)
	// ListAssignmentsDueBy returns assignments with a non-nil Deadline at or
	// before t. The scheduler uses it to find assignments whose deadline has
	// passed.
	ListAssignmentsDueBy(ctx context.Context, t time.Time) ([]*Assignment, error)

	// Roster.
	CreateRosterEntry(ctx context.Context, r *RosterEntry) error
	GetRosterEntry(ctx context.Context, id string) (*RosterEntry, error)
	// FindRosterEntryByUsername matches within a classroom (host is constant per
	// classroom). Returns ErrNotFound if no entry matches.
	FindRosterEntryByUsername(ctx context.Context, classroomID, username string) (*RosterEntry, error)
	UpdateRosterEntry(ctx context.Context, r *RosterEntry) error
	ListRosterEntries(ctx context.Context, classroomID string) ([]*RosterEntry, error)

	// Submissions.
	CreateSubmission(ctx context.Context, s *Submission) error
	GetSubmission(ctx context.Context, id string) (*Submission, error)
	FindSubmission(ctx context.Context, assignmentID, rosterEntryID string) (*Submission, error)
	// FindSubmissionByRepo maps a provisioned repo (host + namespace + name) back to
	// its submission. The webhook receiver uses it to route an incoming push.
	// Returns ErrNotFound when no submission owns that repo.
	FindSubmissionByRepo(ctx context.Context, host adapter.Host, namespace, name string) (*Submission, error)
	// ListSubmissionsByRosterUsername returns every submission belonging to a
	// student, found by joining roster entries on (host, host_username). It powers
	// the student's own-work view. Ordered newest-activity-first.
	ListSubmissionsByRosterUsername(ctx context.Context, host adapter.Host, username string) ([]*Submission, error)
	UpdateSubmission(ctx context.Context, s *Submission) error
	ListSubmissionsByAssignment(ctx context.Context, assignmentID string) ([]*Submission, error)
	ListSubmissionsByClassroom(ctx context.Context, classroomID string) ([]*Submission, error)

	// Grades.
	CreateGrade(ctx context.Context, g *Grade) error
	LatestGradeForSubmission(ctx context.Context, submissionID string) (*Grade, error)
	// ListGradesBySubmission returns the full attempt history for a submission
	// (most recent first), so a student can see how their score evolved.
	ListGradesBySubmission(ctx context.Context, submissionID string) ([]*Grade, error)

	// Grading runs (audit trail of each grading execution).
	CreateGradingRun(ctx context.Context, run *GradingRun) error
	UpdateGradingRun(ctx context.Context, run *GradingRun) error
	ListGradingRunsBySubmission(ctx context.Context, submissionID string) ([]*GradingRun, error)

	// Provisioning jobs (durable queue state).
	// CreateJob is idempotent on IdempotencyKey: a duplicate returns created=false.
	CreateJob(ctx context.Context, j *ProvisioningJob) (created bool, err error)
	// ClaimNextJob atomically takes the oldest runnable pending job and marks it
	// in-progress. Returns ErrNotFound when nothing is runnable.
	ClaimNextJob(ctx context.Context) (*ProvisioningJob, error)
	UpdateJob(ctx context.Context, j *ProvisioningJob) error
}
