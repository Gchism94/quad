// SPDX-License-Identifier: Apache-2.0

package classroom

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// SnapshotFormat is the on-disk layout version, so a future change can be
// detected rather than silently mis-read.
const SnapshotFormat = 1

// Manifest describes a captured snapshot.
type Manifest struct {
	Format        int       `json:"format"`
	Source        string    `json:"source"` // always "github-classroom"
	APIBase       string    `json:"api_base"`
	ClassroomID   int64     `json:"classroom_id"`
	ClassroomName string    `json:"classroom_name"`
	Organization  string    `json:"organization"`
	CapturedAt    time.Time `json:"captured_at"`
	// AssignmentIDs fixes the order assignments are replayed in, so an import
	// from a snapshot is deterministic.
	AssignmentIDs []int64 `json:"assignment_ids"`
}

// AssignmentRecord is one assignment and everyone who accepted it.
type AssignmentRecord struct {
	Assignment Assignment
	Accepted   []AcceptedAssignment
}

// Snapshot is a complete, offline copy of one classroom's metadata.
//
// It exists because the API it came from will not: GitHub deletes Classroom data
// on September 4, 2026. A snapshot captured before then still imports afterwards.
//
// A snapshot contains GitHub usernames and repository names. It contains no legal
// names, because the endpoint carrying them is never requested — see the package
// doc.
type Snapshot struct {
	Manifest    Manifest
	Classroom   Classroom
	Assignments []AssignmentRecord
}

// OrgLogin returns the organization slug the classroom's repositories live under,
// or "" if the classroom was captured without one.
func (s *Snapshot) OrgLogin() string {
	if s.Classroom.Organization == nil {
		return ""
	}
	return s.Classroom.Organization.Login
}

// Fetch reads a whole classroom from the API into a Snapshot. It performs
// 2+2n requests for n assignments and writes nothing to GitHub.
func Fetch(ctx context.Context, c *Client, classroomID int64) (*Snapshot, error) {
	cls, err := c.GetClassroom(ctx, classroomID)
	if err != nil {
		return nil, fmt.Errorf("get classroom %d: %w", classroomID, err)
	}
	list, err := c.ListAssignments(ctx, classroomID)
	if err != nil {
		return nil, fmt.Errorf("list assignments for classroom %d: %w", classroomID, err)
	}

	snap := &Snapshot{
		Classroom: cls,
		Manifest: Manifest{
			Format:        SnapshotFormat,
			Source:        "github-classroom",
			APIBase:       c.baseURL,
			ClassroomID:   cls.ID,
			ClassroomName: cls.Name,
			CapturedAt:    time.Now().UTC(),
		},
	}
	if cls.Organization != nil {
		snap.Manifest.Organization = cls.Organization.Login
	}

	for _, a := range list {
		// The list response omits starter_code_repository; the detail response has
		// it, and is otherwise a superset. Only the detail response is stored.
		detail, err := c.GetAssignment(ctx, a.ID)
		if err != nil {
			return nil, fmt.Errorf("get assignment %d (%s): %w", a.ID, a.Slug, err)
		}
		accepted, err := c.ListAcceptedAssignments(ctx, a.ID)
		if err != nil {
			return nil, fmt.Errorf("list accepted assignments for %d (%s): %w", a.ID, a.Slug, err)
		}
		snap.Assignments = append(snap.Assignments, AssignmentRecord{Assignment: detail, Accepted: accepted})
		snap.Manifest.AssignmentIDs = append(snap.Manifest.AssignmentIDs, a.ID)
	}
	return snap, nil
}

// Snapshot file layout:
//
//	<dir>/manifest.json
//	<dir>/classroom.json
//	<dir>/assignments/<id>/assignment.json
//	<dir>/assignments/<id>/accepted-assignments.json
const (
	manifestFile   = "manifest.json"
	classroomFile  = "classroom.json"
	assignmentDir  = "assignments"
	assignmentJSON = "assignment.json"
	acceptedJSON   = "accepted-assignments.json"
)

// Write persists the snapshot under dir, creating it if needed. Existing files
// are overwritten, so re-capturing a classroom into the same directory refreshes
// it.
func (s *Snapshot) Write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, manifestFile), s.Manifest); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, classroomFile), s.Classroom); err != nil {
		return err
	}
	for _, rec := range s.Assignments {
		sub := filepath.Join(dir, assignmentDir, strconv.FormatInt(rec.Assignment.ID, 10))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(sub, assignmentJSON), rec.Assignment); err != nil {
			return err
		}
		accepted := rec.Accepted
		if accepted == nil {
			accepted = []AcceptedAssignment{}
		}
		if err := writeJSON(filepath.Join(sub, acceptedJSON), accepted); err != nil {
			return err
		}
	}
	return nil
}

// ReadSnapshot loads a snapshot previously written by Write.
func ReadSnapshot(dir string) (*Snapshot, error) {
	snap := &Snapshot{}
	if err := readJSON(filepath.Join(dir, manifestFile), &snap.Manifest); err != nil {
		return nil, fmt.Errorf("read snapshot manifest: %w", err)
	}
	if snap.Manifest.Format != SnapshotFormat {
		return nil, fmt.Errorf("snapshot %s: unsupported format %d (this build reads format %d)",
			dir, snap.Manifest.Format, SnapshotFormat)
	}
	if err := readJSON(filepath.Join(dir, classroomFile), &snap.Classroom); err != nil {
		return nil, fmt.Errorf("read snapshot classroom: %w", err)
	}
	for _, id := range snap.Manifest.AssignmentIDs {
		sub := filepath.Join(dir, assignmentDir, strconv.FormatInt(id, 10))
		var rec AssignmentRecord
		if err := readJSON(filepath.Join(sub, assignmentJSON), &rec.Assignment); err != nil {
			return nil, fmt.Errorf("read snapshot assignment %d: %w", id, err)
		}
		if err := readJSON(filepath.Join(sub, acceptedJSON), &rec.Accepted); err != nil {
			return nil, fmt.Errorf("read snapshot accepted assignments for %d: %w", id, err)
		}
		snap.Assignments = append(snap.Assignments, rec)
	}
	return snap, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
