// SPDX-License-Identifier: AGPL-3.0-or-later

package provisioning

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Grader runs grading for a submission. It is satisfied by the grading package's
// Service and injected so the worker need not depend on grading internals.
type Grader interface {
	Grade(ctx context.Context, submissionID string) error
}

// Worker drains the job queue, executing each job against the Git-host adapter
// for the job's classroom. It is the rate-limited choke point in front of every
// host write; jobs are retried with backoff and marked failed after MaxAttempts.
type Worker struct {
	Store    store.Store
	Adapters map[adapter.Host]adapter.Adapter
	Grader   Grader // optional; nil means grade jobs fail with a clear error
	// WebhookBaseURL is the public base URL of this Cairn instance (e.g.
	// https://cairn.example). When set, each provisioned repo gets a push webhook at
	// <base>/webhooks/<host>, so a delivery always reaches the handler that knows
	// how to verify that host (HMAC for github/gitea/forgejo; token for gitlab).
	WebhookBaseURL string
	// WebhookSecrets is the per-host secret the host uses to sign/identify push
	// deliveries, matched by the receiver to verify them. Keyed by adapter.Host; a
	// missing key means the webhook is registered without a secret (unverifiable).
	WebhookSecrets map[adapter.Host]string

	MaxAttempts int                             // default 5
	Backoff     func(attempt int) time.Duration // default capped exponential
	Poll        time.Duration                   // default 2s
	Log         *log.Logger                     // default log.Default()
}

func (w *Worker) maxAttempts() int {
	if w.MaxAttempts > 0 {
		return w.MaxAttempts
	}
	return 5
}

func (w *Worker) backoff(attempt int) time.Duration {
	if w.Backoff != nil {
		return w.Backoff(attempt)
	}
	d := time.Duration(1<<min(attempt, 6)) * time.Second // 2s,4s,...,capped at 64s
	return d
}

func (w *Worker) logger() *log.Logger {
	if w.Log != nil {
		return w.Log
	}
	return log.Default()
}

// Run drains the queue until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	poll := w.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		did, err := w.RunOnce(ctx)
		if err != nil {
			w.logger().Printf("provisioning: %v", err)
		}
		if did {
			continue // keep draining while there is work
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// RunOnce claims and processes at most one job. It reports whether a job was
// claimed (regardless of success), so callers can drain in a loop.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.Store.ClaimNextJob(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	execErr := w.execute(ctx, job)
	job.Attempts++
	switch {
	case execErr == nil:
		job.Status = store.JobSucceeded
		job.LastError = ""
		if JobType(job.Type) == JobCreateRepo {
			w.clearSubmissionError(ctx, job.TargetRef)
		}
	case job.Attempts >= w.maxAttempts():
		job.Status = store.JobFailed
		job.LastError = execErr.Error()
		if JobType(job.Type) == JobCreateRepo {
			w.recordSubmissionFailure(ctx, job.TargetRef, execErr)
		}
	default:
		job.Status = store.JobPending
		job.LastError = execErr.Error()
		job.ScheduledAt = time.Now().Add(w.backoff(job.Attempts))
	}
	if err := w.Store.UpdateJob(ctx, job); err != nil {
		return true, fmt.Errorf("update job %s: %w", job.ID, err)
	}
	return true, execErr
}

func (w *Worker) execute(ctx context.Context, job *store.ProvisioningJob) error {
	switch JobType(job.Type) {
	case JobCreateRepo:
		return w.createRepo(ctx, job.TargetRef)
	case JobLockRepo:
		return w.setLock(ctx, job.TargetRef, true)
	case JobUnlockRepo:
		return w.setLock(ctx, job.TargetRef, false)
	case JobGrade:
		if w.Grader == nil {
			return fmt.Errorf("grade job %s: no grader configured", job.ID)
		}
		return w.Grader.Grade(ctx, job.TargetRef)
	default:
		return fmt.Errorf("unknown job type %q", job.Type)
	}
}

// createRepo provisions the repository for a submission: generate it from the
// assignment template, grant the student write access, and (optionally) ensure a
// push webhook. TargetRef is the submission ID.
func (w *Worker) createRepo(ctx context.Context, submissionID string) error {
	sub, err := w.Store.GetSubmission(ctx, submissionID)
	if err != nil {
		return fmt.Errorf("get submission: %w", err)
	}
	asg, err := w.Store.GetAssignment(ctx, sub.AssignmentID)
	if err != nil {
		return fmt.Errorf("get assignment: %w", err)
	}
	re, err := w.Store.GetRosterEntry(ctx, sub.RosterEntryID)
	if err != nil {
		return fmt.Errorf("get roster entry: %w", err)
	}
	cls, err := w.Store.GetClassroom(ctx, asg.ClassroomID)
	if err != nil {
		return fmt.Errorf("get classroom: %w", err)
	}

	ad := w.Adapters[cls.Host]
	if ad == nil {
		return fmt.Errorf("no adapter configured for host %q", cls.Host)
	}

	ns, err := ad.EnsureNamespace(ctx, cls.HostNamespace)
	if err != nil {
		return fmt.Errorf("ensure namespace %q: %w", cls.HostNamespace, err)
	}
	repoName := asg.Slug + "-" + re.HostUsername
	repo, err := ad.CreateRepoFromTemplate(ctx, asg.TemplateRef, ns, repoName, adapter.CreateRepoOptions{Private: true})
	if err != nil {
		return fmt.Errorf("create repo: %w", err)
	}
	if err := ad.SetCollaborator(ctx, repo, re.HostUsername, adapter.RoleWrite); err != nil {
		return fmt.Errorf("add collaborator: %w", err)
	}
	if w.WebhookBaseURL != "" {
		// Best-effort: a missing webhook should not fail provisioning. The URL is
		// per-host (<base>/webhooks/<host>) so deliveries reach the right verifier;
		// the per-host secret lets the receiver authenticate them.
		hookURL := strings.TrimRight(w.WebhookBaseURL, "/") + "/webhooks/" + string(cls.Host)
		_ = ad.EnsureWebhook(ctx, repo, adapter.WebhookSpec{
			URL:    hookURL,
			Secret: w.WebhookSecrets[cls.Host],
			Events: []string{"push"},
		})
	}

	sub.Repo = repo
	sub.Status = "active"
	return w.Store.UpdateSubmission(ctx, sub)
}

// recordSubmissionFailure marks the submission as "failed" with a truncated
// error string. Best-effort: if the load/update fails, we log and move on so
// the job-status update always proceeds.
func (w *Worker) recordSubmissionFailure(ctx context.Context, submissionID string, execErr error) {
	sub, err := w.Store.GetSubmission(ctx, submissionID)
	if err != nil {
		w.logger().Printf("provisioning: record failure: get submission %s: %v", submissionID, err)
		return
	}
	sub.Status = "failed"
	msg := execErr.Error()
	const maxErrLen = 500
	if len(msg) > maxErrLen {
		msg = msg[:maxErrLen]
	}
	sub.LastError = msg
	if err := w.Store.UpdateSubmission(ctx, sub); err != nil {
		w.logger().Printf("provisioning: record failure: update submission %s: %v", submissionID, err)
	}
}

// clearSubmissionError resets a submission back to "active" with an empty
// LastError after a successful provisioning run. Best-effort.
func (w *Worker) clearSubmissionError(ctx context.Context, submissionID string) {
	sub, err := w.Store.GetSubmission(ctx, submissionID)
	if err != nil {
		return
	}
	sub.LastError = ""
	// Status is already set to "active" by createRepo before UpdateSubmission;
	// this path handles the case where the job succeeded after previous failures.
	_ = w.Store.UpdateSubmission(ctx, sub)
}

// setLock locks (deadline reached) or unlocks (reopened) a submission's repo.
// TargetRef is the submission ID. A submission with no provisioned repo is a
// no-op, so a lock job that races ahead of provisioning fails safe. LockRepo and
// UnlockRepo are themselves idempotent on the host, so a repeated job is benign.
func (w *Worker) setLock(ctx context.Context, submissionID string, lock bool) error {
	sub, err := w.Store.GetSubmission(ctx, submissionID)
	if err != nil {
		return fmt.Errorf("get submission: %w", err)
	}
	if sub.Repo.Name == "" {
		return nil // nothing provisioned yet; nothing to lock
	}
	ad := w.Adapters[sub.Repo.Host]
	if ad == nil {
		return fmt.Errorf("no adapter configured for host %q", sub.Repo.Host)
	}
	if lock {
		if err := ad.LockRepo(ctx, sub.Repo); err != nil {
			return fmt.Errorf("lock repo: %w", err)
		}
		sub.Status = "locked"
	} else {
		if err := ad.UnlockRepo(ctx, sub.Repo); err != nil {
			return fmt.Errorf("unlock repo: %w", err)
		}
		sub.Status = "active"
	}
	return w.Store.UpdateSubmission(ctx, sub)
}
