// SPDX-License-Identifier: Apache-2.0

package classroom

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleSnapshot() *Snapshot {
	deadline := time.Date(2026, 2, 7, 1, 0, 0, 0, time.UTC)
	return &Snapshot{
		Manifest: Manifest{
			Format:        SnapshotFormat,
			Source:        "github-classroom",
			APIBase:       DefaultAPIBase,
			ClassroomID:   77,
			ClassroomName: "CS-101",
			Organization:  "CS-101-F26",
			CapturedAt:    time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
			AssignmentIDs: []int64{501, 502},
		},
		Classroom: Classroom{
			ID: 77, Name: "CS-101",
			Organization: &Organization{ID: 9, Login: "CS-101-F26"},
		},
		Assignments: []AssignmentRecord{
			{
				Assignment: Assignment{ID: 501, Title: "HW 1", Slug: "hw-01", Type: TypeIndividual, Deadline: &deadline},
				Accepted: []AcceptedAssignment{{
					ID:         900,
					Students:   []Student{{ID: 1, Login: "student01"}},
					Repository: Repository{ID: 2, Name: "hw-01-student01", FullName: "CS-101-F26/hw-01-student01"},
					Grade:      "10/90",
				}},
			},
			{
				// A group assignment with no acceptances at all.
				Assignment: Assignment{ID: 502, Title: "Final", Slug: "final-project", Type: TypeGroup},
			},
		},
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := sampleSnapshot()
	if err := want.Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	// An assignment with no acceptances is written as [] and reads back as an
	// empty slice rather than nil, so normalize before comparing.
	if got.Assignments[1].Accepted == nil {
		t.Error("an assignment with no acceptances should read back as an empty slice")
	}
	want.Assignments[1].Accepted = []AcceptedAssignment{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed the snapshot:\ngot  %+v\nwant %+v", got, want)
	}
	if got.OrgLogin() != "CS-101-F26" {
		t.Errorf("OrgLogin = %q", got.OrgLogin())
	}
}

func TestSnapshotWriteIsRecapturable(t *testing.T) {
	dir := t.TempDir()
	snap := sampleSnapshot()
	if err := snap.Write(dir); err != nil {
		t.Fatal(err)
	}
	// Re-capturing into the same directory refreshes it rather than failing.
	snap.Classroom.Name = "CS-101 (renamed)"
	snap.Manifest.ClassroomName = "CS-101 (renamed)"
	if err := snap.Write(dir); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	got, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Classroom.Name != "CS-101 (renamed)" {
		t.Errorf("name = %q", got.Classroom.Name)
	}
}

func TestSnapshotLayoutIsReadableJSON(t *testing.T) {
	dir := t.TempDir()
	if err := sampleSnapshot().Write(dir); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"manifest.json",
		"classroom.json",
		filepath.Join("assignments", "501", "assignment.json"),
		filepath.Join("assignments", "501", "accepted-assignments.json"),
	} {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Errorf("missing %s: %v", rel, err)
			continue
		}
		if !strings.Contains(string(b), "\n  ") {
			t.Errorf("%s is not indented; a snapshot outlives the API and should stay human-readable", rel)
		}
	}
}

func TestReadSnapshotRejectsUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	snap := sampleSnapshot()
	snap.Manifest.Format = SnapshotFormat + 1
	if err := snap.Write(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(dir); err == nil {
		t.Error("expected an error for an unsupported snapshot format")
	}
}

func TestReadSnapshotMissingDirectory(t *testing.T) {
	if _, err := ReadSnapshot(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error for a missing snapshot directory")
	}
}
