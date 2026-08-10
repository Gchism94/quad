// SPDX-License-Identifier: AGPL-3.0-or-later

package provisioning

import (
	"context"
	"log"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
)

// PurgeJob implements Cairn's purge-after-confirmed-export retention policy
// (PRIVACY.md's "Open items" / data-destinations.md §8): a grade is deleted
// once its export has been explicitly confirmed AND RetentionDays has
// elapsed since. A grade never confirmed is never touched, regardless of age.
//
// RetentionDays <= 0 means retention is unconfigured — every tick is a no-op,
// so purge is effectively disabled until CAIRN_GRADE_RETENTION_DAYS is set.
// `cairn doctor` surfaces this state; see internal/doctor.CheckRetention.
type PurgeJob struct {
	Store         store.Store
	RetentionDays int
	Interval      time.Duration // default 24h — this is not the per-minute deadline scheduler
	Log           *log.Logger   // default log.Default()
}

func (p *PurgeJob) interval() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return 24 * time.Hour
}

func (p *PurgeJob) logger() *log.Logger {
	if p.Log != nil {
		return p.Log
	}
	return log.Default()
}

// Run purges on each tick until ctx is cancelled.
func (p *PurgeJob) Run(ctx context.Context) {
	t := time.NewTicker(p.interval())
	defer t.Stop()
	for {
		if grades, runs, err := p.RunOnce(ctx, time.Now()); err != nil {
			p.logger().Printf("purge: %v", err)
		} else if grades > 0 {
			p.logger().Printf("purge: removed %d grade(s), %d grading run(s)", grades, runs)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce purges every grade whose export was confirmed at or before
// now - RetentionDays. now is explicit so tests can drive it deterministically.
func (p *PurgeJob) RunOnce(ctx context.Context, now time.Time) (gradesPurged, runsPurged int, err error) {
	if p.RetentionDays <= 0 {
		return 0, 0, nil
	}
	cutoff := now.AddDate(0, 0, -p.RetentionDays)
	return p.Store.PurgeExportedGrades(ctx, cutoff)
}
