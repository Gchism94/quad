// SPDX-License-Identifier: Apache-2.0

// Package classroom is a read-only client for GitHub Classroom's REST API, used
// to migrate a course off Classroom before its August 28, 2026 shutdown (final
// data deletion September 4, 2026).
//
// This package holds every GitHub-specific fact about the migration: the API
// endpoints, the shape of their JSON, the `<slug>-<username>` repository naming
// convention, and the "awarded/available" grade-string format. Callers receive
// plain data and need no knowledge of GitHub. Like the rest of pkg/adapter it
// depends on the standard library only and never imports Cairn internals.
//
// Two properties are deliberate and load-bearing:
//
//   - It is strictly read-only. Nothing here writes to GitHub.
//   - It never requests GET /assignments/:id/grades. That endpoint's
//     roster_identifier field carries a student's legal name, which Cairn's data
//     model forbids storing; it is also incomplete (observed returning 4 rows for
//     22 accepted students) and its points_available was "0" where the grade
//     string on accepted_assignments read "10/90". Scores come from
//     AcceptedAssignment.Grade instead. See docs/ghc-import.md.
//
// Authentication requires a *user* token with the read:org scope — e.g.
// `gh auth token` or a classic PAT. The GitHub App installation token used by the
// sibling github adapter cannot read the Classroom API, which is user-scoped.
package classroom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIBase is GitHub's public API root.
const DefaultAPIBase = "https://api.github.com"

// perPage is the page size used for every paginated listing. GitHub caps it at
// 100; classroom listings are small, so this is usually a single request.
const perPage = 100

// Config configures a Client.
type Config struct {
	// Token is a user access token with the read:org scope. Required.
	Token string
	// BaseURL overrides the API root (GHES, or a test server). Optional.
	BaseURL string
	// HTTP overrides the HTTP client. Optional.
	HTTP *http.Client
}

// Client is a read-only GitHub Classroom API client. It is safe for concurrent
// use.
type Client struct {
	httpc   *http.Client
	baseURL string
	token   string
}

// New constructs a Client. It fails only on missing credentials; no network call
// is made here.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("classroom: a user token is required (set CAIRN_GHC_TOKEN, e.g. to `gh auth token`)")
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultAPIBase
	}
	httpc := cfg.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{httpc: httpc, baseURL: strings.TrimRight(base, "/"), token: cfg.Token}, nil
}

// --- wire types -----------------------------------------------------------

// Organization is the GitHub org backing a classroom. Its Login is the namespace
// every assignment repository lives under, and it outlives the Classroom
// shutdown.
type Organization struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	HTMLURL string `json:"html_url"`
}

// Classroom is a GitHub Classroom. Organization is populated by GetClassroom but
// not by ListClassrooms.
type Classroom struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Archived     bool          `json:"archived"`
	URL          string        `json:"url"`
	Organization *Organization `json:"organization,omitempty"`
}

// Repository is a GitHub repository as Classroom reports it.
type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// Assignment types, as reported in Assignment.Type.
const (
	TypeIndividual = "individual"
	TypeGroup      = "group"
)

// Assignment is a Classroom assignment. StarterCodeRepository is populated by
// GetAssignment but not by ListAssignments.
type Assignment struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Slug   string `json:"slug"`
	Type   string `json:"type"`
	Editor string `json:"editor"`
	// Deadline is nil when the assignment has none.
	Deadline    *time.Time `json:"deadline"`
	Accepted    int        `json:"accepted"`
	Submissions int        `json:"submissions"`
	Passing     int        `json:"passing"`
	MaxTeams    *int       `json:"max_teams"`
	MaxMembers  *int       `json:"max_members"`
	PublicRepo  bool       `json:"public_repo"`
	// StarterCodeRepository is the template the assignment was generated from. It
	// may be nil (deleted, or never set).
	StarterCodeRepository *Repository `json:"starter_code_repository,omitempty"`
	Classroom             *Classroom  `json:"classroom,omitempty"`
}

// IsGroup reports whether this is a team assignment, whose repository is shared
// by several students and whose name is not derivable from any username.
func (a Assignment) IsGroup() bool { return a.Type == TypeGroup }

// Student is a GitHub account that accepted an assignment. Login is the only
// durable identity Cairn stores.
type Student struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	HTMLURL string `json:"html_url"`
}

// AcceptedAssignment binds students to the repository created for them. It is the
// authoritative import source: its row count matches Assignment.Accepted exactly,
// and it carries login, repository, and grade on one object — so no repository
// name has to be inferred from a naming convention.
type AcceptedAssignment struct {
	ID          int64      `json:"id"`
	Submitted   bool       `json:"submitted"`
	Passing     bool       `json:"passing"`
	CommitCount int        `json:"commit_count"`
	Students    []Student  `json:"students"`
	Repository  Repository `json:"repository"`
	// Grade is "awarded/available" (e.g. "10/90"), or empty when GitHub reports
	// null. Parse it with ParseGrade.
	Grade string `json:"grade"`
}

// --- API calls ------------------------------------------------------------

// ListClassrooms returns every classroom the token can see.
func (c *Client) ListClassrooms(ctx context.Context) ([]Classroom, error) {
	return listPaged[Classroom](ctx, c, "/classrooms")
}

// GetClassroom returns one classroom, including its backing organization.
func (c *Client) GetClassroom(ctx context.Context, id int64) (Classroom, error) {
	var out Classroom
	err := c.get(ctx, "/classrooms/"+strconv.FormatInt(id, 10), &out)
	return out, err
}

// ListAssignments returns a classroom's assignments. The returned assignments do
// not carry starter-code repositories; call GetAssignment for that.
func (c *Client) ListAssignments(ctx context.Context, classroomID int64) ([]Assignment, error) {
	return listPaged[Assignment](ctx, c, "/classrooms/"+strconv.FormatInt(classroomID, 10)+"/assignments")
}

// GetAssignment returns one assignment, including its starter-code repository.
func (c *Client) GetAssignment(ctx context.Context, id int64) (Assignment, error) {
	var out Assignment
	err := c.get(ctx, "/assignments/"+strconv.FormatInt(id, 10), &out)
	return out, err
}

// AuthenticatedUser returns the account the token belongs to. The importer uses
// it to attribute imported rows to the operator running the import.
func (c *Client) AuthenticatedUser(ctx context.Context) (Student, error) {
	var out Student
	err := c.get(ctx, "/user", &out)
	return out, err
}

// ListAcceptedAssignments returns every acceptance of an assignment: which
// students, which repository, which grade.
func (c *Client) ListAcceptedAssignments(ctx context.Context, assignmentID int64) ([]AcceptedAssignment, error) {
	return listPaged[AcceptedAssignment](ctx, c, "/assignments/"+strconv.FormatInt(assignmentID, 10)+"/accepted_assignments")
}

// --- helpers --------------------------------------------------------------

// ParseGrade splits Classroom's "awarded/available" grade string. ok is false for
// an empty, malformed, or non-numeric value — callers should skip those rather
// than record a zero score.
func ParseGrade(s string) (awarded, available float64, ok bool) {
	num, den, found := strings.Cut(strings.TrimSpace(s), "/")
	if !found {
		return 0, 0, false
	}
	a, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return 0, 0, false
	}
	b, err := strconv.ParseFloat(strings.TrimSpace(den), 64)
	if err != nil {
		return 0, 0, false
	}
	return a, b, true
}

// SplitFullName splits an "owner/name" repository full name. It returns empty
// strings when the input is not in that form.
func SplitFullName(fullName string) (owner, name string) {
	owner, name, found := strings.Cut(strings.TrimSpace(fullName), "/")
	if !found || owner == "" || name == "" {
		return "", ""
	}
	return owner, name
}

// --- HTTP plumbing --------------------------------------------------------

// APIError is a non-2xx response from the Classroom API.
type APIError struct {
	Status int
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("classroom api: status %d for %s — the Classroom API needs a *user* token with the read:org scope "+
			"(a GitHub App installation token will not work); try CAIRN_GHC_TOKEN=$(gh auth token): %s", e.Status, e.Path, e.Body)
	case http.StatusNotFound:
		return fmt.Sprintf("classroom api: %s not found — check the id, and note that Classroom data is deleted after September 4, 2026: %s", e.Path, e.Body)
	default:
		return fmt.Sprintf("classroom api: status %d for %s: %s", e.Status, e.Path, e.Body)
	}
}

// StatusOf extracts the HTTP status from an *APIError, or 0 for other errors.
func StatusOf(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// get performs a GET and decodes a 200 body into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Path: path, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// listPaged walks a paginated collection until a short page ends it. The Classroom
// API paginates with page/per_page and sends no Link header, so page length is the
// only available terminator.
func listPaged[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		var batch []T
		q := fmt.Sprintf("%s?page=%d&per_page=%d", path, page, perPage)
		if err := c.get(ctx, q, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			return all, nil
		}
	}
}
