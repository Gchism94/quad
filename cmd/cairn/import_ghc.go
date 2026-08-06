// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/id"
	"github.com/EduCloud-Ecosystem/cairn/internal/importer"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter/github/classroom"
)

const importGHCHelp = `cairn import ghc — import a course from GitHub Classroom

GitHub Classroom shuts down August 28, 2026 and its data is deleted September 4.
Organizations and repositories are unaffected, so this command copies the
perishable metadata into Cairn and points at the repositories where they already
are. It never writes to GitHub.

The Classroom API needs a *user* token with the read:org scope — the GitHub App
credentials Cairn uses elsewhere will not work here:

    export CAIRN_GHC_TOKEN="$(gh auth token)"

Typical use:

    cairn import ghc --list
    cairn import ghc --classroom 298020 --dry-run --snapshot ./ghc-298020
    cairn import ghc --classroom 298020 --snapshot ./ghc-298020

Every live run captures a snapshot. After the shutdown, that snapshot is the only
way in:

    cairn import ghc --from ./ghc-298020

Flags:
`

type importGHCFlags struct {
	classroomID     int64
	from            string
	snapshot        string
	dryRun          bool
	list            bool
	org             string
	createdBy       string
	joinPolicy      string
	noGrades        bool
	skipGroup       bool
	retroactiveLock bool
	token           string
	apiBase         string
}

func runImportGHC(argv []string) error {
	var f importGHCFlags
	fs := flag.NewFlagSet("import ghc", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), importGHCHelp)
		fs.PrintDefaults()
	}
	fs.Int64Var(&f.classroomID, "classroom", 0, "GitHub Classroom id to import (see --list)")
	fs.StringVar(&f.from, "from", "", "import from a previously captured snapshot directory instead of the API")
	fs.StringVar(&f.snapshot, "snapshot", "", "directory to write the captured snapshot to (default ghc-snapshot-<id>)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print the full plan and write nothing to the Cairn store")
	fs.BoolVar(&f.list, "list", false, "list the classrooms this token can see, then exit")
	fs.StringVar(&f.org, "org", "", "organization slug, overriding the one recorded in the classroom")
	fs.StringVar(&f.createdBy, "created-by", "", "GitHub username to attribute the import to (default: the token's own account)")
	fs.StringVar(&f.joinPolicy, "join-policy", store.ClassroomJoinPolicyRoster, "join policy for a newly created classroom: roster or open")
	fs.BoolVar(&f.noGrades, "no-grades", false, "do not import historical grades")
	fs.BoolVar(&f.skipGroup, "skip-group", false, "skip group assignments entirely")
	fs.BoolVar(&f.retroactiveLock, "retroactive-lock", false,
		"allow already-passed deadlines to lock the imported repositories (a mass write to the Git host; off by default)")
	fs.StringVar(&f.token, "token", "", "Classroom API token (default: CAIRN_GHC_TOKEN, GITHUB_TOKEN, or GH_TOKEN)")
	fs.StringVar(&f.apiBase, "api-base", "", "GitHub API base URL (default https://api.github.com)")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	switch f.joinPolicy {
	case store.ClassroomJoinPolicyRoster, store.ClassroomJoinPolicyOpen:
	default:
		return fmt.Errorf("--join-policy must be %q or %q", store.ClassroomJoinPolicyRoster, store.ClassroomJoinPolicyOpen)
	}

	ctx := context.Background()

	if f.list {
		return listClassrooms(ctx, f)
	}

	snap, err := loadSnapshot(ctx, f)
	if err != nil {
		return err
	}

	plan, warnings, err := importer.FromGitHubClassroom(snap, f.org)
	if err != nil {
		return err
	}
	plan.JoinPolicy = f.joinPolicy
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	st, storeKind, err := openStore(ctx)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	createdBy, err := resolveOperator(ctx, st, f)
	if err != nil {
		return err
	}

	if f.dryRun {
		fmt.Printf("\nDRY RUN — reading %s, writing nothing.\n\n", storeKind)
	} else {
		fmt.Printf("\nImporting into %s.\n\n", storeKind)
	}

	res, err := importer.Apply(ctx, st, plan, importer.Options{
		DryRun:          f.dryRun,
		CreatedBy:       createdBy,
		ImportGrades:    !f.noGrades,
		SkipGroup:       f.skipGroup,
		RetroactiveLock: f.retroactiveLock,
		Now:             time.Now().UTC(),
		Out:             os.Stdout,
	})
	if err != nil {
		return err
	}

	fmt.Printf("\n%s", res.Summary())
	if f.dryRun {
		fmt.Printf("\nNothing was written. Re-run without --dry-run to apply.\n")
	}
	return nil
}

// loadSnapshot resolves the importer's input: a snapshot directory on disk, or a
// live capture from the Classroom API (which is also written to disk).
func loadSnapshot(ctx context.Context, f importGHCFlags) (*classroom.Snapshot, error) {
	if f.from != "" {
		if f.classroomID != 0 {
			return nil, errors.New("pass either --from or --classroom, not both")
		}
		snap, err := classroom.ReadSnapshot(f.from)
		if err != nil {
			return nil, err
		}
		fmt.Printf("snapshot:   %s (captured %s)\n", f.from, snap.Manifest.CapturedAt.Format(time.RFC3339))
		return snap, nil
	}
	if f.classroomID == 0 {
		return nil, errors.New("--classroom is required (run --list to find it), or pass --from <snapshot dir>")
	}
	c, err := newClassroomClient(f)
	if err != nil {
		return nil, err
	}
	snap, err := classroom.Fetch(ctx, c, f.classroomID)
	if err != nil {
		return nil, err
	}
	dir := f.snapshot
	if dir == "" {
		dir = fmt.Sprintf("ghc-snapshot-%d", f.classroomID)
	}
	if err := snap.Write(dir); err != nil {
		return nil, fmt.Errorf("write snapshot: %w", err)
	}
	fmt.Printf("snapshot:   captured %d assignment(s) to %s\n", len(snap.Assignments), dir)
	return snap, nil
}

func listClassrooms(ctx context.Context, f importGHCFlags) error {
	c, err := newClassroomClient(f)
	if err != nil {
		return err
	}
	all, err := c.ListClassrooms(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tARCHIVED")
	for _, cl := range all {
		fmt.Fprintf(w, "%d\t%s\t%t\n", cl.ID, cl.Name, cl.Archived)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n%d classroom(s). Import one with: cairn import ghc --classroom <id> --dry-run\n", len(all))
	return nil
}

func newClassroomClient(f importGHCFlags) (*classroom.Client, error) {
	tok := f.token
	for _, env := range []string{"CAIRN_GHC_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if tok != "" {
			break
		}
		tok = os.Getenv(env)
	}
	if strings.TrimSpace(tok) == "" {
		return nil, errors.New("no Classroom API token: set CAIRN_GHC_TOKEN (e.g. to `gh auth token`) or pass --token. " +
			"It must be a user token with the read:org scope; Cairn's GitHub App credentials cannot read the Classroom API")
	}
	return classroom.New(classroom.Config{Token: tok, BaseURL: f.apiBase})
}

// resolveOperator returns the User.ID to attribute a created classroom to,
// upserting the operator row if needed. It returns "" when no operator can be
// determined — an unattributed import, which is better than inventing one.
func resolveOperator(ctx context.Context, st store.Store, f importGHCFlags) (string, error) {
	username := f.createdBy
	if username == "" && f.from == "" {
		// Live import: attribute it to whoever the token belongs to.
		c, err := newClassroomClient(f)
		if err != nil {
			return "", err
		}
		me, err := c.AuthenticatedUser(ctx)
		if err != nil {
			return "", fmt.Errorf("identify token owner (pass --created-by to skip this lookup): %w", err)
		}
		username = me.Login
	}
	if username == "" {
		return "", nil
	}

	existing, err := st.FindUserByHostUsername(ctx, adapter.HostGitHub, username)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", fmt.Errorf("look up operator %s: %w", username, err)
	}
	u := &store.User{
		ID:           id.New(),
		Host:         adapter.HostGitHub,
		HostUsername: username,
		CreatedAt:    time.Now().UTC(),
	}
	if f.dryRun {
		return u.ID, nil
	}
	if err := st.CreateUser(ctx, u); err != nil {
		return "", fmt.Errorf("create operator %s: %w", username, err)
	}
	return u.ID, nil
}
