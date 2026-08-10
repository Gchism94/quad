// SPDX-License-Identifier: AGPL-3.0-or-later

package postgres

import (
	"reflect"
	"testing"
)

// The regression guard for CC-P5: Migrate used to read "0001_init.up.sql" by
// name, literally, so 0002-0004 were embedded but never applied. This test
// pins the file list migrationFiles produces, with no database and no
// network, so it runs in ordinary CI rather than only under -tags postgres.
func TestMigrationFilesAreOrderedAndComplete(t *testing.T) {
	got, err := migrationFiles()
	if err != nil {
		t.Fatalf("migrationFiles: %v", err)
	}
	want := []string{
		"0001_init.up.sql",
		"0002_submission_last_error.up.sql",
		"0003_classroom_join_policy.up.sql",
		"0004_student_loop_indexes.up.sql",
		"0005_grade_export_confirmed.up.sql",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrationFiles() = %v, want %v", got, want)
	}
}
