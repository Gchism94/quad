// SPDX-License-Identifier: AGPL-3.0-or-later

package provisioning

import (
	"context"
	"log"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
)

// Scheduler enforces assignment deadlines. On each tick it finds assignments
// whose deadline has passed and enqueues a lock job for every provisioned
// submission repo. The enqueue is idempotent (key "lock:<submissionID>"), so a
// repo is auto-locked at most once no matter how many ticks observe the passed
// deadline — and an instructor who later unlocks for an extension is not fought
// by the scheduler, because that key is already spent.
type Scheduler struct {
	Store    store.Store
	Queue    Queue
	Interval time.Duration // default 1m
	Log      *log.Logger   // default log.Default()
}

func (s *Scheduler) interval() time.Duration {
	if s.Interval > 0 {
		return s.Interval
	}
	return time.Minute
}

func (s *Scheduler) logger() *log.Logger {
	if s.Log != nil {
		return s.Log
	}
	return log.Default()
}

// Run enforces deadlines until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.interval())
	defer t.Stop()
	for {
		if n, err := s.RunOnce(ctx, time.Now()); err != nil {
			s.logger().Printf("scheduler: %v", err)
		} else if n > 0 {
			s.logger().Printf("scheduler: enqueued %d lock job(s)", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce enqueues lock jobs for every provisioned submission of every
// assignment due at or before now. It reports how many jobs it enqueued (before
// idempotent de-duplication, which the queue applies). now is explicit so tests
// can drive it deterministically.
func (s *Scheduler) RunOnce(ctx context.Context, now time.Time) (int, error) {
	due, err := s.Store.ListAssignmentsDueBy(ctx, now)
	if err != nil {
		return 0, err
	}
	enqueued := 0
	for _, a := range due {
		subs, err := s.Store.ListSubmissionsByAssignment(ctx, a.ID)
		if err != nil {
			return enqueued, err
		}
		for _, sub := range subs {
			if sub.Repo.Name == "" {
				continue // not provisioned; nothing to lock
			}
			if err := s.Queue.Enqueue(ctx, JobLockRepo, sub.ID, "lock:"+sub.ID); err != nil {
				return enqueued, err
			}
			enqueued++
		}
	}
	return enqueued, nil
}
