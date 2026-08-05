// SPDX-License-Identifier: AGPL-3.0-or-later

package provisioning

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

type recordingQueue struct {
	calls []struct {
		Type   JobType
		Target string
		Idem   string
	}
}

func (q *recordingQueue) Enqueue(_ context.Context, t JobType, target, idem string) error {
	q.calls = append(q.calls, struct {
		Type   JobType
		Target string
		Idem   string
	}{t, target, idem})
	return nil
}

func provisioned(id, assignmentID, name string) *store.Submission {
	return &store.Submission{
		ID:           id,
		AssignmentID: assignmentID,
		Repo:         adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org", Name: name},
	}
}

func TestSchedulerLocksOnlyPastDueProvisionedRepos(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	now := time.Date(2025, 12, 2, 0, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	// Past-due assignment: one provisioned submission, one not yet provisioned.
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1", Deadline: &past})
	_ = st.CreateSubmission(ctx, provisioned("s1", "a1", "hw1-bob"))
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s2", AssignmentID: "a1"}) // no repo

	// Future-due assignment: must be ignored.
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a2", ClassroomID: "c1", Slug: "hw2", Deadline: &future})
	_ = st.CreateSubmission(ctx, provisioned("s3", "a2", "hw2-bob"))

	// No-deadline assignment: must be ignored.
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a3", ClassroomID: "c1", Slug: "hw3"})
	_ = st.CreateSubmission(ctx, provisioned("s4", "a3", "hw3-bob"))

	q := &recordingQueue{}
	sch := &Scheduler{Store: st, Queue: q}
	n, err := sch.RunOnce(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("enqueued %d, want 1", n)
	}
	c := q.calls[0]
	if c.Type != JobLockRepo || c.Target != "s1" || c.Idem != "lock:s1" {
		t.Fatalf("unexpected enqueue: %+v", c)
	}
}

func TestSchedulerLocksEachRepoAtMostOnce(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	past := time.Now().Add(-time.Hour)
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1", Deadline: &past})
	_ = st.CreateSubmission(ctx, provisioned("s1", "a1", "hw1-bob"))

	sch := &Scheduler{Store: st, Queue: NewService(st)} // real store-backed queue
	for i := 0; i < 3; i++ {
		if _, err := sch.RunOnce(ctx, time.Now()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	// Exactly one lock job should have been persisted.
	j, err := st.ClaimNextJob(ctx)
	if err != nil {
		t.Fatalf("expected one job: %v", err)
	}
	if JobType(j.Type) != JobLockRepo || j.TargetRef != "s1" {
		t.Fatalf("job = %+v", j)
	}
	if _, err := st.ClaimNextJob(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected only one job, got %v", err)
	}
}
