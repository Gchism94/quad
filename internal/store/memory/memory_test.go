// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/storetest"
)

func TestMemoryConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return New() })
}

// TestCreateSubmissionDedup verifies that concurrent calls with the same
// (AssignmentID, RosterEntryID) produce exactly one successful insert and all
// others return ErrConflict, and that exactly one submission exists afterwards.
// Run with -race to catch any locking gaps.
func TestCreateSubmissionDedup(t *testing.T) {
	const N = 20
	m := New()
	ctx := context.Background()

	// Seed parents so foreign-key-aware implementations work.
	_ = m.CreateClassroom(ctx, &store.Classroom{ID: "c1", Name: "CS101", Host: "github", HostNamespace: "org"})
	_ = m.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Title: "HW1", Slug: "hw-1", Type: store.AssignmentIndividual, GradingSpec: "grading.json"})
	_ = m.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: "github", HostUsername: "octocat", Status: store.RosterInvited})

	var (
		mu       sync.Mutex
		okCount  int
		errOther error
	)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub := &store.Submission{
				ID:            fmt.Sprintf("s%d", i),
				AssignmentID:  "a1",
				RosterEntryID: "r1",
				Status:        "provisioning",
			}
			err := m.CreateSubmission(ctx, sub)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, store.ErrConflict):
				// expected for all but the winner
			default:
				errOther = err
			}
		}(i)
	}
	wg.Wait()

	if errOther != nil {
		t.Fatalf("unexpected error: %v", errOther)
	}
	if okCount != 1 {
		t.Fatalf("okCount = %d, want exactly 1", okCount)
	}

	// Exactly one submission should be stored.
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, s := range m.submissions {
		if s.AssignmentID == "a1" && s.RosterEntryID == "r1" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("submissions in store = %d, want 1", count)
	}
}
