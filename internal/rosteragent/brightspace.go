// SPDX-License-Identifier: AGPL-3.0-or-later

package rosteragent

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Brightspace has two connectors, and which one an instructor can actually use
// is decided by their institution, not by us.
//
// Verified against D2L's current developer documentation on 2026-08-09:
// registering an application for the Valence API is done through the **Manage
// Extensibility** admin tool and requires the **"Can Manage API Applications"**
// permission, which is an administrator capability by default — an instructor
// does not have it. The newer OAuth 2 Client Credentials flow is further
// gated: "a Brightspace administration must prepare your service user and role
// ahead of time." There is no instructor-self-serve personal access token of
// the kind Canvas offers.
//
// So BrightspaceExport is the path that works on day one, and BrightspaceAPI is
// the path that works after IT grants access. Both produce the same RosterRow.

// BrightspaceExport reads a Classlist export the instructor already has — the
// CSV that Brightspace's own Classlist tool produces via "Email Classlist" /
// export, which an instructor can obtain without any API permission at all.
//
// This is the primary Brightspace path precisely because it needs no
// institutional approval: the instructor already has this data on screen.
type BrightspaceExport struct {
	// Path is the CSV file. It is read locally and never uploaded.
	Path string
}

func (b *BrightspaceExport) Name() string { return "brightspace-export" }

// FetchRoster parses the export. Brightspace's column names vary by
// configuration and locale, so headers are matched by meaning rather than by a
// fixed position — a wrong guess here would silently mis-assign students.
func (b *BrightspaceExport) FetchRoster(_ context.Context) ([]RosterRow, error) {
	f, err := os.Open(b.Path)
	if err != nil {
		return nil, fmt.Errorf("cannot read Brightspace export %q: %w\n\n%s", b.Path, err, exportHowTo)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // exports carry ragged trailing columns
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("cannot parse %q as CSV: %w\n\n%s", b.Path, err, exportHowTo)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("%q has no data rows\n\n%s", b.Path, exportHowTo)
	}

	idx := headerIndex(records[0])
	if idx.name < 0 && idx.first < 0 && idx.last < 0 {
		return nil, fmt.Errorf(
			"could not find a name column in %q (headers: %s)\n\n%s",
			b.Path, strings.Join(records[0], ", "), exportHowTo)
	}

	rows := make([]RosterRow, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := RosterRow{
			Name:      composeName(rec, idx),
			Email:     at(rec, idx.email),
			LMSUserID: at(rec, idx.userID),
		}
		if row.Name == "" && row.Email == "" {
			continue // blank trailing line
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%q contained no students\n\n%s", b.Path, exportHowTo)
	}
	return rows, nil
}

const exportHowTo = `To get a Brightspace classlist export:
  Course → Classlist → Enrolled Users, then use the export/print view and save
  it as CSV. Any export with a name column works; an email column is optional
  (it is hashed locally and never sent).

If you cannot export, use Cairn's manual bulk entry instead:
  dashboard → classroom → Roster → "Bulk add".`

type colIndex struct{ name, first, last, email, userID int }

func headerIndex(header []string) colIndex {
	idx := colIndex{-1, -1, -1, -1, -1}
	for i, h := range header {
		switch normalizeHeader(h) {
		case "name", "fullname", "displayname", "student":
			idx.name = i
		case "firstname", "first", "givenname":
			idx.first = i
		case "lastname", "last", "surname", "familyname":
			idx.last = i
		case "email", "emailaddress", "externalemail":
			idx.email = i
		case "username", "orgdefinedid", "userid", "identifier":
			// OrgDefinedId is the institution's student ID. It is read only to
			// label audit output; it is never transmitted.
			if idx.userID < 0 {
				idx.userID = i
			}
		}
	}
	return idx
}

func normalizeHeader(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func composeName(rec []string, idx colIndex) string {
	if n := at(rec, idx.name); n != "" {
		return n
	}
	first, last := at(rec, idx.first), at(rec, idx.last)
	switch {
	case first != "" && last != "":
		return first + " " + last
	case last != "":
		return last
	default:
		return first
	}
}

func at(rec []string, i int) string {
	if i < 0 || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(strings.Trim(rec[i], `"`))
}

// BrightspaceAPI reads the roster from the Valence Classlist endpoint.
//
// This requires an OAuth 2 access token whose application was registered by a
// Brightspace **administrator** (see the package comment). It is provided for
// institutions that have already granted access; it is not the path a lone
// instructor can take on day one.
type BrightspaceAPI struct {
	// BaseURL is the institution's Brightspace host, e.g. https://d2l.example.edu
	BaseURL string
	// AccessToken is an OAuth 2 bearer token. It is used for this call only and
	// is never written to the audit log.
	AccessToken string
	// OrgUnitID is the course's Brightspace org unit.
	OrgUnitID string
	// LEVersion is the Learning Environment API version, e.g. "1.67".
	LEVersion string

	HTTP *http.Client
}

func (b *BrightspaceAPI) Name() string { return "brightspace-api" }

// classlistUser mirrors the fields of Valence's ClasslistUser that this agent
// uses. Brightspace can suppress Email/FirstName/LastName per org-unit
// configuration, so every field is treated as optional.
type classlistUser struct {
	Identifier   string `json:"Identifier"`
	DisplayName  string `json:"DisplayName"`
	Email        string `json:"Email"`
	FirstName    string `json:"FirstName"`
	LastName     string `json:"LastName"`
	Username     string `json:"Username"`
	OrgDefinedID string `json:"OrgDefinedId"`
}

func (b *BrightspaceAPI) FetchRoster(ctx context.Context) ([]RosterRow, error) {
	if b.BaseURL == "" || b.AccessToken == "" || b.OrgUnitID == "" {
		return nil, fmt.Errorf("brightspace API needs --base-url, --org-unit and a token\n\n%s", apiHowTo)
	}
	version := b.LEVersion
	if version == "" {
		version = "1.67"
	}
	httpc := b.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}

	url := fmt.Sprintf("%s/d2l/api/le/%s/%s/classlist/",
		strings.TrimRight(b.BaseURL, "/"), version, b.OrgUnitID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.AccessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brightspace request failed: %w\n\n%s", err, apiHowTo)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf(
			"brightspace refused the token (HTTP %d). This is the common case: API access is\n"+
				"granted by a Brightspace administrator, not self-issued by an instructor.\n\n%s",
			resp.StatusCode, apiHowTo)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("brightspace returned HTTP %d: %s\n\n%s",
			resp.StatusCode, strings.TrimSpace(string(body)), apiHowTo)
	}

	var users []classlistUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("cannot decode classlist response: %w", err)
	}

	rows := make([]RosterRow, 0, len(users))
	for _, u := range users {
		name := u.DisplayName
		if name == "" {
			name = strings.TrimSpace(u.FirstName + " " + u.LastName)
		}
		id := u.OrgDefinedID
		if id == "" {
			id = u.Identifier
		}
		if name == "" && u.Email == "" {
			continue
		}
		rows = append(rows, RosterRow{Name: name, Email: u.Email, LMSUserID: id})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("brightspace returned no students for org unit %s\n\n%s", b.OrgUnitID, apiHowTo)
	}
	return rows, nil
}

const apiHowTo = `Brightspace API access is administrator-granted, not self-serve:
an application must be registered in the Manage Extensibility admin tool by
someone with the "Can Manage API Applications" permission. Ask your Brightspace
administrator, or use either path that needs no approval:

  --export <file.csv>   parse a classlist export you already have
  manual bulk entry     dashboard → classroom → Roster → "Bulk add"`
