// SPDX-License-Identifier: AGPL-3.0-or-later

package provisioning

import (
	"context"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/id"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
)

// Service is the store-backed producer side of the queue.
type Service struct {
	store store.Store
}

// NewService returns a Service that persists jobs to s.
func NewService(s store.Store) *Service { return &Service{store: s} }

// Compile-time guarantee that *Service satisfies Queue.
var _ Queue = (*Service)(nil)

// Enqueue persists a job. A repeated call with the same idempotencyKey is a
// no-op, so callers may safely retry.
func (s *Service) Enqueue(ctx context.Context, t JobType, targetRef, idempotencyKey string) error {
	job := &store.ProvisioningJob{
		ID:             id.New(),
		Type:           string(t),
		TargetRef:      targetRef,
		Status:         store.JobPending,
		IdempotencyKey: idempotencyKey,
		ScheduledAt:    time.Now(),
	}
	_, err := s.store.CreateJob(ctx, job)
	return err
}
