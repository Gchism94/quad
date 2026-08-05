// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store contains Cairn's domain models and the persistence that backs them.
//
// PRIVACY: the models below deliberately omit student legal names, SIS IDs, and
// plaintext student emails. The control plane's identity anchor for a student is
// their Git-host username (RosterEntry.HostUsername). See DESIGN.md sections 5 and 6.
package store

import (
	"time"

	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

const (
	// ClassroomJoinPolicyOpen allows any authenticated student on the host to
	// self-enroll. ClassroomJoinPolicyRoster restricts enrollment to usernames
	// explicitly added via the roster API; unknown usernames receive a 403.
	ClassroomJoinPolicyOpen   = "open"
	ClassroomJoinPolicyRoster = "roster"
)

// Classroom is a course backed by a Git-host org/group.
type Classroom struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"` // course label — metadata, not student PII
	Host          adapter.Host `json:"host"`
	HostNamespace string       `json:"host_namespace"`
	// JoinPolicy controls self-enrollment: "open" (default) or "roster".
	JoinPolicy string    `json:"join_policy"`
	CreatedBy  string    `json:"created_by"` // User.ID
	CreatedAt  time.Time `json:"created_at"`
}

// User is a platform operator (instructor or TA): an account holder. Storing an
// operator's own email is fine; the privacy constraints apply to students.
type User struct {
	ID           string       `json:"id"`
	Host         adapter.Host `json:"host"`
	HostUserID   string       `json:"host_user_id"`
	HostUsername string       `json:"host_username"`
	Email        string       `json:"email"`
	CreatedAt    time.Time    `json:"created_at"`
}

// AssignmentType distinguishes individual from group assignments.
type AssignmentType string

const (
	AssignmentIndividual AssignmentType = "individual"
	AssignmentGroup      AssignmentType = "group"
)

// Assignment is a piece of work generated from a template repo.
type Assignment struct {
	ID           string              `json:"id"`
	ClassroomID  string              `json:"classroom_id"`
	Title        string              `json:"title"`
	Slug         string              `json:"slug"`
	TemplateRef  adapter.TemplateRef `json:"template"`
	Type         AssignmentType      `json:"type"`
	Deadline     *time.Time          `json:"deadline,omitempty"` // nil means no deadline
	GradingSpec  string              `json:"grading_spec"`       // path inside the template repo
	AccessPolicy string              `json:"access_policy,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
}

// RosterStatus is the lifecycle state of a roster entry.
type RosterStatus string

const (
	RosterInvited RosterStatus = "invited"
	RosterActive  RosterStatus = "active"
	RosterRemoved RosterStatus = "removed"
)

// RosterEntry is the privacy-critical record: it binds a Git-host username to a
// classroom. It MUST NOT carry a legal name, SIS ID, or plaintext email.
type RosterEntry struct {
	ID           string       `json:"id"`
	ClassroomID  string       `json:"classroom_id"`
	Host         adapter.Host `json:"host"`
	HostUsername string       `json:"host_username"` // the durable identity anchor
	// EmailHash is an OPTIONAL salted, one-way hash used only for client-side
	// re-matching against an LMS pull. It is never reversible to an address and
	// is never the plaintext email.
	EmailHash string       `json:"email_hash,omitempty"`
	Status    RosterStatus `json:"status"`
	ClaimedAt *time.Time   `json:"claimed_at,omitempty"`
}

// Submission is a student's repo for an assignment.
type Submission struct {
	ID             string          `json:"id"`
	AssignmentID   string          `json:"assignment_id"`
	RosterEntryID  string          `json:"roster_entry_id"`
	Repo           adapter.RepoRef `json:"repo"`
	LatestCommit   string          `json:"latest_commit,omitempty"`
	LastActivityAt *time.Time      `json:"last_activity_at,omitempty"`
	Status         string          `json:"status"`
	LastError      string          `json:"last_error,omitempty"` // non-empty when status=="failed"
}

// Grade is, when joined to its Submission and RosterEntry, an education record.
// Minimization reduces exposure but does not remove the institution's
// obligations; self-hosting keeps these rows inside the institution's own
// infrastructure. See DESIGN.md section 10.
type Grade struct {
	ID           string    `json:"id"`
	SubmissionID string    `json:"submission_id"`
	Score        float64   `json:"score"`
	MaxScore     float64   `json:"max_score"`
	Breakdown    []byte    `json:"breakdown,omitempty"` // JSON: per-test results
	RunID        string    `json:"run_id,omitempty"`
	GradedAt     time.Time `json:"graded_at"`
}

// JobStatus is the lifecycle state of a provisioning job.
type JobStatus string

const (
	JobPending    JobStatus = "pending"
	JobInProgress JobStatus = "in_progress"
	JobSucceeded  JobStatus = "succeeded"
	JobFailed     JobStatus = "failed"
)

// ProvisioningJob is durable queue state. IdempotencyKey guarantees retries
// never double-create host resources.
type ProvisioningJob struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	TargetRef      string    `json:"target_ref"`
	Status         JobStatus `json:"status"`
	Attempts       int       `json:"attempts"`
	IdempotencyKey string    `json:"idempotency_key"`
	LastError      string    `json:"last_error,omitempty"`
	ScheduledAt    time.Time `json:"scheduled_at"`
}

// GradingRun records a single execution of an assignment's grading spec.
type GradingRun struct {
	ID           string     `json:"id"`
	SubmissionID string     `json:"submission_id"`
	Status       string     `json:"status"`
	Runner       string     `json:"runner,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Result       []byte     `json:"result,omitempty"` // JSON
	LogsRef      string     `json:"logs_ref,omitempty"`
}
