// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/rosteragent"
)

const rosterPullHelp = `cairn roster pull — build a Cairn roster from your LMS, locally

Reads your class roster from the LMS, matches each student to a Git-host
username on THIS machine, and sends only {username, email_hash} to Cairn.
Student names and email addresses never leave your computer.

Brightspace access, verified against D2L's developer documentation 2026-08-09:
registering an application for the Valence API needs the "Can Manage API
Applications" permission in the Manage Extensibility admin tool — an
ADMINISTRATOR capability. There is no instructor-self-serve API token like
Canvas's. So the export path is the one that works without asking anyone:

    # 1. Course → Classlist → export/print view → save as CSV
    cairn roster pull --lms brightspace --export classlist.csv \
        --candidates usernames.txt --classroom <id> --dry-run

    # 2. review the plan, then send it
    cairn roster pull --lms brightspace --export classlist.csv \
        --candidates usernames.txt --classroom <id> --server https://cairn.example.edu

If your institution HAS granted API access, use it instead of --export:

    cairn roster pull --lms brightspace --base-url https://d2l.example.edu \
        --org-unit 12345 --token "$BRIGHTSPACE_TOKEN" --classroom <id>

--candidates is a file of the Git usernames to match against, one per line,
optionally "username,Full Name" to match on the student's name:

    octocat,Jane Doe
    hubot

Matches are reported in four tiers: exact (used automatically), needs-confirm
(you are asked, unless --yes), ambiguous (several candidates tie — you pick from
a numbered list; --yes and --dry-run never auto-pick, since no rule can choose
safely), and unmatched (never guessed — listed so you can add them by hand).
Nothing is sent until you have seen the plan.

If your LMS has no connector, this command says so and points you at manual
bulk entry rather than failing quietly.
`

type rosterPullFlags struct {
	lms        string
	exportPath string
	baseURL    string
	orgUnit    string
	token      string
	leVersion  string
	candidates string
	classroom  string
	server     string
	auditPath  string
	dryRun     bool
	assumeYes  bool
}

func runRosterPull(argv []string) error {
	var f rosterPullFlags
	fs := flag.NewFlagSet("roster pull", flag.ContinueOnError)
	fs.Usage = func() { fmt.Print(rosterPullHelp); fmt.Println("\nFlags:"); fs.PrintDefaults() }
	fs.StringVar(&f.lms, "lms", "brightspace", "LMS to pull from (brightspace)")
	fs.StringVar(&f.exportPath, "export", "", "path to a classlist CSV export (no API access needed)")
	fs.StringVar(&f.baseURL, "base-url", "", "Brightspace host, e.g. https://d2l.example.edu (API mode)")
	fs.StringVar(&f.orgUnit, "org-unit", "", "Brightspace org unit ID of the course (API mode)")
	fs.StringVar(&f.token, "token", os.Getenv("BRIGHTSPACE_TOKEN"), "OAuth 2 access token (API mode; or $BRIGHTSPACE_TOKEN)")
	fs.StringVar(&f.leVersion, "le-version", "1.67", "Brightspace Learning Environment API version")
	fs.StringVar(&f.candidates, "candidates", "", "file of Git usernames, one per line, optionally \"username,Full Name\"")
	fs.StringVar(&f.classroom, "classroom", "", "Cairn classroom ID to add the roster to")
	fs.StringVar(&f.server, "server", "http://localhost:8080", "Cairn server base URL")
	fs.StringVar(&f.auditPath, "audit", "", "write the audit log to this file as well as stdout")
	fs.BoolVar(&f.dryRun, "dry-run", false, "show what would be sent and send nothing")
	fs.BoolVar(&f.assumeYes, "yes", false, "accept needs-confirm matches without prompting")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	conn, err := connectorFor(f)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	audit := &auditLog{}
	audit.printf("cairn roster pull — audit log")
	audit.printf("when:      %s", time.Now().Format(time.RFC3339))
	audit.printf("connector: %s", conn.Name())

	rows, err := conn.FetchRoster(ctx)
	if err != nil {
		// Honest failure: say what happened and where to go instead.
		return fmt.Errorf("could not read the roster from %s.\n\n%w", f.lms, err)
	}
	audit.printf("fetched:   %d student(s) from the LMS", len(rows))

	candidates, err := loadCandidates(f.candidates)
	if err != nil {
		return err
	}
	audit.printf("candidates: %d Git username(s) from %s", len(candidates), f.candidates)
	audit.printf("")

	matches := rosteragent.MatchRoster(rows, candidates)
	// Everything above this line happened locally; nothing has been sent.

	accepted, skipped := reviewMatches(audit, matches, f.assumeYes, f.dryRun)

	if f.classroom == "" {
		audit.printf("")
		audit.printf("No --classroom given, so nothing was sent. Re-run with --classroom <id>.")
		audit.flush(f.auditPath)
		return nil
	}

	payload, unsent := rosteragent.BuildPayload(f.classroom, accepted)
	skipped = append(skipped, unsent...)

	audit.printf("")
	audit.printf("payload:   %d entr(ies) — usernames and salted email hashes only", len(payload.Entries))
	if len(skipped) > 0 {
		audit.printf("NOT SENT:  %d student(s) had no confirmed username (listed above).", len(skipped))
		audit.printf("           This roster is PARTIAL. Add them via dashboard → Roster → \"Bulk add\".")
	}
	if len(payload.Entries) == 0 {
		audit.printf("")
		audit.printf("Nothing to send.")
		audit.flush(f.auditPath)
		return nil
	}

	if f.dryRun {
		body, _ := json.MarshalIndent(payload, "", "  ")
		audit.printf("")
		audit.printf("DRY RUN — this is the exact request body, and it was NOT sent:")
		audit.printf("POST %s/classrooms/%s/roster/bulk", strings.TrimRight(f.server, "/"), f.classroom)
		audit.printf("%s", body)
		audit.flush(f.auditPath)
		return nil
	}

	if err := submitBulk(ctx, f, payload, audit); err != nil {
		audit.flush(f.auditPath)
		return err
	}
	audit.flush(f.auditPath)
	return nil
}

func connectorFor(f rosterPullFlags) (rosteragent.Connector, error) {
	switch strings.ToLower(f.lms) {
	case "brightspace", "d2l":
		if f.exportPath != "" {
			return &rosteragent.BrightspaceExport{Path: f.exportPath}, nil
		}
		if f.baseURL != "" || f.token != "" {
			return &rosteragent.BrightspaceAPI{
				BaseURL: f.baseURL, AccessToken: f.token,
				OrgUnitID: f.orgUnit, LEVersion: f.leVersion,
			}, nil
		}
		return nil, fmt.Errorf(
			"brightspace needs either --export <classlist.csv> (works without any API access)\n" +
				"or --base-url/--org-unit/--token (needs API access your Brightspace administrator\n" +
				"must grant — it is not self-serve for instructors).\n\n" +
				"No export either? Use manual bulk entry: dashboard → classroom → Roster → \"Bulk add\".")
	default:
		return nil, rosteragent.ErrNoConnector{LMS: f.lms}
	}
}

func loadCandidates(path string) ([]rosteragent.Candidate, error) {
	if path == "" {
		return nil, fmt.Errorf(
			"--candidates is required: a file of the Git usernames to match against,\n" +
				"one per line, optionally \"username,Full Name\".\n\n" +
				"Matching happens on this machine, so the agent needs to know which usernames\n" +
				"are possible. Ask students for their Git username, or export it from wherever\n" +
				"you already collect it.")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read --candidates %q: %w", path, err)
	}
	var out []rosteragent.Candidate
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		username, full, _ := strings.Cut(line, ",")
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		out = append(out, rosteragent.Candidate{Username: username, FullName: strings.TrimSpace(full)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--candidates %q contained no usernames", path)
	}
	return out, nil
}

// reviewMatches prints every match and asks about the uncertain ones. Names are
// shown on the instructor's own terminal — that is the point of local matching.
func reviewMatches(audit *auditLog, matches []rosteragent.Match, assumeYes, dryRun bool) (accepted, skipped []rosteragent.Match) {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LMS STUDENT\tGIT USERNAME\tMATCH\tWHY")
	sorted := append([]rosteragent.Match(nil), matches...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Row.Name < sorted[j].Row.Name })
	for _, m := range sorted {
		u := m.Username
		if u == "" {
			u = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", m.Row.Name, u, m.Status, m.Why)
	}
	tw.Flush()
	audit.printf("%s", strings.TrimRight(buf.String(), "\n"))

	reader := bufio.NewReader(os.Stdin)
	for _, m := range matches {
		switch m.Status {
		case rosteragent.MatchExact:
			accepted = append(accepted, m)
		case rosteragent.MatchNeedsConfirm:
			if assumeYes || dryRun {
				accepted = append(accepted, m)
				continue
			}
			fmt.Printf("Confirm: %q → %s ? [y/N] ", m.Row.Name, m.Username)
			line, _ := reader.ReadString('\n')
			if strings.EqualFold(strings.TrimSpace(line), "y") {
				accepted = append(accepted, m)
			} else {
				audit.printf("declined:  %s → %s", m.Row.Name, m.Username)
				skipped = append(skipped, m)
			}

		case rosteragent.MatchAmbiguous:
			// A tie is resolvable, not a dead end: list the candidates and let
			// the instructor pick. --yes and --dry-run must NOT auto-pick — the
			// whole reason this is ambiguous is that no rule can choose safely,
			// so bulk-accepting would enrol a coin flip.
			if assumeYes || dryRun {
				audit.printf("ambiguous: %s — needs an interactive choice; not resolved (%s)",
					m.Row.Name, m.Why)
				skipped = append(skipped, m)
				continue
			}
			resolved := promptAmbiguous(reader, &m)
			if resolved {
				audit.printf("resolved:  %s → %s", m.Row.Name, m.Username)
				accepted = append(accepted, m)
			} else {
				audit.printf("skipped:   %s — ambiguity left unresolved", m.Row.Name)
				skipped = append(skipped, m)
			}

		default:
			skipped = append(skipped, m)
		}
	}
	return accepted, skipped
}

// promptAmbiguous lists the tied candidates and applies the instructor's choice
// to m. It reports whether the row was resolved; an empty answer, "s", or an
// unparseable one leaves the row unresolved, which the caller reports as
// skipped. Selection is by number so a mistyped username cannot enrol someone
// who was never offered — and Match.Resolve rejects that case anyway.
func promptAmbiguous(reader *bufio.Reader, m *rosteragent.Match) bool {
	fmt.Printf("\nAmbiguous: %q matches %d candidates.\n", m.Row.Name, len(m.Candidates))
	if m.Row.Email != "" {
		// The email is the instructor's best disambiguator and it is already on
		// their screen from the LMS; it is never transmitted.
		fmt.Printf("  LMS email: %s\n", m.Row.Email)
	}
	for i, c := range m.Candidates {
		label := c.Username
		if c.FullName != "" {
			label += "  (" + c.FullName + ")"
		}
		fmt.Printf("  [%d] %s\n", i+1, label)
	}
	fmt.Print("Choose 1-", len(m.Candidates), ", or Enter to skip: ")

	line, _ := reader.ReadString('\n')
	choice := strings.TrimSpace(line)
	if choice == "" || strings.EqualFold(choice, "s") {
		return false
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(m.Candidates) {
		fmt.Printf("  not a listed option; skipping %q\n", m.Row.Name)
		return false
	}
	if err := m.Resolve(m.Candidates[n-1].Username); err != nil {
		fmt.Printf("  %v; skipping\n", err)
		return false
	}
	return true
}

func submitBulk(ctx context.Context, f rosterPullFlags, payload rosteragent.BulkRequest, audit *auditLog) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/classrooms/%s/roster/bulk", strings.TrimRight(f.server, "/"), f.classroom)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Cairn at %s: %w", f.server, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cairn returned HTTP %d: %s\n\n"+
			"The roster was NOT added. Nothing partial was written.",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out struct {
		Created        int `json:"created"`
		AlreadyPresent int `json:"already_present"`
		Errors         int `json:"errors"`
		Results        []struct {
			Username string `json:"username"`
			Status   string `json:"status"`
			Error    string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("cairn returned an unreadable response: %w", err)
	}

	audit.printf("")
	audit.printf("sent:      POST %s", url)
	audit.printf("result:    %d created, %d already present, %d error(s)",
		out.Created, out.AlreadyPresent, out.Errors)
	for _, r := range out.Results {
		if r.Status == "error" {
			audit.printf("  ERROR    %s: %s", r.Username, r.Error)
		}
	}
	return nil
}

// auditLog collects the run's record. It holds usernames and counts — never a
// token, and never a plaintext email.
type auditLog struct{ lines []string }

func (a *auditLog) printf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	a.lines = append(a.lines, line)
	fmt.Println(line)
}

func (a *auditLog) flush(path string) {
	if path == "" {
		return
	}
	content := strings.Join(a.lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write audit log to %s: %v\n", path, err)
		return
	}
	fmt.Printf("\naudit log written to %s\n", path)
}
