// SPDX-License-Identifier: AGPL-3.0-or-later

// Package api is the Cairn control-plane HTTP server.
package api

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/id"
	"github.com/EduCloud-Ecosystem/cairn/internal/identity"
	"github.com/EduCloud-Ecosystem/cairn/internal/provisioning"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// Options holds the Server's dependencies.
type Options struct {
	Store store.Store
	Queue provisioning.Queue
	// Resolvers maps each Git host to its OAuth resolver. Multiple hosts may be
	// registered at once — students self-enroll via the resolver for their
	// classroom's host, and operators log in via the LoginHost resolver.
	Resolvers map[adapter.Host]identity.Resolver
	// LoginHost identifies which resolver handles operator login. When empty, the
	// server picks whichever resolver is available (GitHub preferred).
	LoginHost adapter.Host
	// WebDir, if set, is a directory of built dashboard assets (web/dist) served
	// at the root with SPA fallback, so the API and UI ship as one binary.
	WebDir string
	// AuthEnabled gates operator authentication. When false (the default), the
	// management API is open and a synthetic operator is used — convenient for
	// local development, but every deployment should enable it.
	AuthEnabled bool
	// AdminUsers is the allowlist of host usernames permitted to operate this
	// instance (only consulted when AuthEnabled).
	AdminUsers []string
	// CookieSecure sets the Secure flag on the session cookie; enable it behind HTTPS.
	CookieSecure bool
	// GraderConfigured reports whether a grading runner has been wired up. When
	// false, POST /assignments/{id}/grade returns 409 immediately instead of
	// enqueueing jobs that would instantly fail in the worker.
	GraderConfigured bool
	// Adapters is the set of configured host adapters. handleCreateClassroom
	// validates a classroom's host against these so a classroom can only be
	// created for a host Cairn can actually provision against — deriving the
	// allowlist from runtime registration keeps it correct as hosts are added
	// (e.g. both "forgejo" and "gitea" once the Gitea-family adapter is wired).
	// The student API also uses them for RepoWebURL links.
	Adapters map[adapter.Host]adapter.Adapter
	// WebhookSecrets is the per-host secret used to verify incoming push-webhook
	// HMAC signatures. A host with no secret here has its deliveries rejected.
	WebhookSecrets map[adapter.Host]string
}

// Server routes and serves the control-plane API.
type Server struct {
	store     store.Store
	queue     provisioning.Queue
	resolvers map[adapter.Host]identity.Resolver
	adapters  map[adapter.Host]adapter.Adapter
	// provisionHosts is the set of hosts with a registered adapter; classroom
	// creation is restricted to these. Derived from Options.Adapters at New().
	provisionHosts map[adapter.Host]bool
	webhookSecrets map[adapter.Host]string
	loginHost      adapter.Host
	webDir         string
	authEnabled    bool
	admins         map[string]bool
	cookieSecure   bool
	mux            *http.ServeMux

	graderConfigured bool

	stMu   sync.Mutex
	states map[string]authFlow // OAuth state -> pending flow

	sessMu   sync.Mutex
	sessions map[string]session // opaque token -> operator session
}

// authFlow records what a pending OAuth state is for: a student claim or an
// operator login. Both arrive on the same callback and are told apart by state.
type authFlow struct {
	kind         string       // "claim" or "login"
	assignmentID string       // set for "claim"
	host         adapter.Host // which resolver to use at callback time
	created      time.Time
}

// session is an authenticated session (kept in memory; users re-authenticate
// after a restart). Both operators and students get sessions in the same map, so
// isOperator MUST be checked before granting operator access — otherwise a
// student session would satisfy operator routes (privilege escalation).
type session struct {
	userID     string       // operator User.ID; empty for students (not Users)
	username   string       // host username (the identity anchor)
	host       adapter.Host // the host this identity belongs to
	created    time.Time
	isOperator bool // true only for allowlisted operator logins
}

// New constructs a Server with routes registered.
func New(opts Options) *Server {
	admins := make(map[string]bool, len(opts.AdminUsers))
	for _, u := range opts.AdminUsers {
		if u != "" {
			admins[u] = true
		}
	}
	provisionHosts := make(map[adapter.Host]bool, len(opts.Adapters))
	for h := range opts.Adapters {
		provisionHosts[h] = true
	}
	s := &Server{
		store:            opts.Store,
		queue:            opts.Queue,
		resolvers:        opts.Resolvers,
		adapters:         opts.Adapters,
		provisionHosts:   provisionHosts,
		webhookSecrets:   opts.WebhookSecrets,
		loginHost:        opts.LoginHost,
		webDir:           opts.WebDir,
		authEnabled:      opts.AuthEnabled,
		admins:           admins,
		cookieSecure:     opts.CookieSecure,
		graderConfigured: opts.GraderConfigured,
		mux:              http.NewServeMux(),
		states:           map[string]authFlow{},
		sessions:         map[string]session{},
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	// Public.
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /auth/callback", s.handleCallback)
	s.mux.HandleFunc("GET /auth/me", s.handleAuthMe)
	s.mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /assignments/{id}/accept", s.handleAccept) // student entry
	s.mux.HandleFunc("POST /webhooks/{host}", s.handleWebhook)       // host push deliveries
	s.mux.HandleFunc("GET /student/login", s.handleStudentLogin)     // returning student

	// Student surface — authenticated by session (currentIdentity), scoped to the
	// caller's own work; NOT admin-gated. /me serves the page itself (public so the
	// page can render a sign-in link); the data routes require a session.
	s.mux.HandleFunc("GET /me", s.handleStudentPage)
	s.mux.HandleFunc("GET /me/work", s.handleMyWork)
	s.mux.HandleFunc("GET /me/work/{submissionID}", s.handleMyWorkDetail)

	// Operator-only (the whole management surface).
	protect := s.requireOperator
	s.mux.HandleFunc("GET /classrooms", protect(s.handleListClassrooms))
	s.mux.HandleFunc("POST /classrooms", protect(s.handleCreateClassroom))
	s.mux.HandleFunc("GET /classrooms/{id}/roster", protect(s.handleListRoster))
	s.mux.HandleFunc("POST /classrooms/{id}/roster", protect(s.handleAddRoster))
	s.mux.HandleFunc("GET /classrooms/{id}/assignments", protect(s.handleListAssignments))
	s.mux.HandleFunc("POST /classrooms/{id}/assignments", protect(s.handleCreateAssignment))
	s.mux.HandleFunc("GET /classrooms/{id}/grades.csv", protect(s.handleGradesCSV))
	s.mux.HandleFunc("GET /assignments/{id}/submissions", protect(s.handleListSubmissions))
	s.mux.HandleFunc("PATCH /assignments/{id}/deadline", protect(s.handleSetDeadline))
	s.mux.HandleFunc("POST /assignments/{id}/lock", protect(s.handleLock))
	s.mux.HandleFunc("POST /assignments/{id}/unlock", protect(s.handleUnlock))
	s.mux.HandleFunc("POST /assignments/{id}/grade", protect(s.handleGrade))

	// Serve the built dashboard last and only if configured. Because Go 1.22's
	// ServeMux gives more specific patterns precedence, every API route above
	// still wins; this catch-all handles "/", static assets, and SPA fallback.
	if s.webDir != "" {
		s.mux.HandleFunc("GET /", s.studentRootRedirect(staticSPAHandler(s.webDir)))
	} else {
		// No dashboard: serve a small inline status page at exactly "/".
		// All other unknown paths remain 404.
		s.mux.HandleFunc("GET /{$}", s.studentRootRedirect(s.handleStatusPage))
	}
}

// ctxKey is the private type for request-context values.
type ctxKey int

const operatorKey ctxKey = iota

const (
	maxRequestBody = 1 << 20          // 1 MiB cap on request bodies
	authFlowTTL    = 10 * time.Minute // OAuth state validity window
	sessionTTL     = 8 * time.Hour    // operator session lifetime
)

// operatorFrom returns the authenticated operator attached by requireOperator.
func operatorFrom(ctx context.Context) (*store.User, bool) {
	op, ok := ctx.Value(operatorKey).(*store.User)
	return op, ok
}

// pruneStatesLocked removes OAuth states older than authFlowTTL.
// Caller must hold s.stMu.
func (s *Server) pruneStatesLocked(now time.Time) {
	for k, f := range s.states {
		if now.Sub(f.created) > authFlowTTL {
			delete(s.states, k)
		}
	}
}

// pruneSessionsLocked removes operator sessions older than sessionTTL.
// Caller must hold s.sessMu.
func (s *Server) pruneSessionsLocked(now time.Time) {
	for k, sess := range s.sessions {
		if now.Sub(sess.created) > sessionTTL {
			delete(s.sessions, k)
		}
	}
}

// requireOperator gates a handler on an authenticated operator. When auth is
// disabled it injects a synthetic operator (empty ID -> NULL created_by) so the
// open/dev path behaves consistently.
func (s *Server) requireOperator(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authEnabled {
			ctx := context.WithValue(r.Context(), operatorKey, &store.User{HostUsername: "dev"})
			next(w, r.WithContext(ctx))
			return
		}
		op, ok := s.operatorFromCookie(r)
		if !ok {
			// If a session cookie was present but invalid/expired, clear it so the
			// browser stops resending a dead token on every request.
			if _, err := r.Cookie(sessionCookie); err == nil {
				http.SetCookie(w, &http.Cookie{
					Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
					MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: s.cookieSecure,
				})
			}
			httpError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), operatorKey, op)))
	}
}

func (s *Server) operatorFromCookie(r *http.Request) (*store.User, bool) {
	sess, ok := s.sessionFromCookie(r)
	if !ok || !sess.isOperator {
		// A non-operator (student) session must not satisfy operator routes.
		return nil, false
	}
	return &store.User{ID: sess.userID, Host: sess.host, HostUsername: sess.username}, true
}

// sessionFromCookie returns the live session for the request's cookie, expiring
// it if past TTL. It does not distinguish operator from student.
func (s *Server) sessionFromCookie(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	sess, ok := s.sessions[c.Value]
	if ok && time.Since(sess.created) > sessionTTL {
		delete(s.sessions, c.Value)
		return session{}, false
	}
	return sess, ok
}

// currentIdentity returns the host + username of any authenticated session
// (operator or student). It is the basis for student-scoped, own-data-only routes.
func (s *Server) currentIdentity(r *http.Request) (host adapter.Host, username string, ok bool) {
	sess, ok := s.sessionFromCookie(r)
	if !ok {
		return "", "", false
	}
	return sess.host, sess.username, true
}

const sessionCookie = "cairn_session"

func newSessionToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// handleLogin begins operator OAuth (no-op redirect when auth is disabled).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	resolver, ok := s.resolvers[s.loginHost]
	if !ok {
		httpError(w, http.StatusInternalServerError, "operator login is not configured")
		return
	}
	state := id.New()
	now := time.Now()
	s.stMu.Lock()
	s.pruneStatesLocked(now)
	s.states[state] = authFlow{kind: "login", host: s.loginHost, created: now}
	s.stMu.Unlock()
	http.Redirect(w, r, resolver.AuthorizeURL(state), http.StatusFound)
}

// handleStudentLogin begins OAuth for a returning student. Unlike operator login
// it applies no admin allowlist — any account on the host may sign in to view its
// own work. Scope: the primary configured login host. (Per-host student login for
// multi-host deployments is a future refinement; the accept flow already sets a
// correct per-host session at enrollment time.)
func (s *Server) handleStudentLogin(w http.ResponseWriter, r *http.Request) {
	resolver, ok := s.resolvers[s.loginHost]
	if !ok {
		httpError(w, http.StatusInternalServerError, "student login is not configured")
		return
	}
	state := id.New()
	now := time.Now()
	s.stMu.Lock()
	s.pruneStatesLocked(now)
	s.states[state] = authFlow{kind: "student_login", host: s.loginHost, created: now}
	s.stMu.Unlock()
	http.Redirect(w, r, resolver.AuthorizeURL(state), http.StatusFound)
}

// handleAuthMe reports the current operator. With auth disabled it returns a
// synthetic operator so the dashboard always renders.
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled {
		writeJSON(w, http.StatusOK, map[string]string{"username": "dev", "auth": "disabled"})
		return
	}
	op, ok := s.operatorFromCookie(r)
	if !ok {
		httpError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": op.ID, "username": op.HostUsername, "auth": "enabled"})
}

// handleLogout clears the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessMu.Lock()
		delete(s.sessions, c.Value)
		s.sessMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: s.cookieSecure,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

// studentRootRedirect sends a signed-in student who lands on "/" to their own
// page instead of the instructor console. The console resolves identity through
// GET /auth/me, which is operator-only by design, so a perfectly valid student
// session renders as "not signed in" on a screen built for instructors — a dead
// end with no link back to /me.
//
// Only the exact root is redirected. The SPA handler is a catch-all that also
// serves static assets and SPA fallback routes, and redirecting those would
// break the dashboard for anyone holding a student session.
//
// Operators and unauthenticated visitors always fall through to next.
func (s *Server) studentRootRedirect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			next(w, r)
			return
		}
		// A session that is present and not an operator is a student. An expired
		// or absent cookie is not ok, so anonymous visitors fall through.
		if sess, ok := s.sessionFromCookie(r); ok && !sess.isOperator {
			http.Redirect(w, r, "/me", http.StatusFound)
			return
		}
		next(w, r)
	}
}

// staticSPAHandler serves files from dir, falling back to index.html for paths
// that don't resolve to a file (so a single-page app can own client routing).
// http.Dir sanitizes the request path, so directory traversal is not possible —
// an invalid path simply falls through to index.html.
func staticSPAHandler(dir string) http.HandlerFunc {
	fsys := http.Dir(dir)
	fileServer := http.FileServer(fsys)
	index := filepath.Join(dir, "index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if f, err := fsys.Open(r.URL.Path); err == nil {
			defer f.Close()
			if info, serr := f.Stat(); serr == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, index)
	}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleStatusPage serves a minimal HTML status page at "/" when no web dir is
// configured. It is public (like /healthz) and requires no JS frameworks.
func (s *Server) handleStatusPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Cairn</title>
<style>body{font-family:sans-serif;max-width:600px;margin:3rem auto;padding:0 1rem}
code{background:#f4f4f4;padding:.1em .3em;border-radius:3px}</style></head>
<body>
<h1>Cairn is running.</h1>
<p id="auth">Checking session…</p>
<ul>
  <li><a href="/auth/login">Operator login</a></li>
  <li><a href="/student/login">Student sign-in</a></li>
  <li><a href="/healthz">Health check</a></li>
</ul>
<p><em>Dashboard not mounted — set <code>CAIRN_WEB_DIR=web/dist</code> and restart.</em></p>
<script>
fetch('/auth/me').then(r=>r.json()).then(d=>{
  var el=document.getElementById('auth');
  if(d.username)el.textContent='Logged in as '+d.username+(d.auth==='disabled'?' (auth disabled)':'');
  else el.textContent='Not logged in.';
}).catch(function(){document.getElementById('auth').textContent='';});
</script>
</body></html>
`)
}

func (s *Server) handleListClassrooms(w http.ResponseWriter, r *http.Request) {
	cs, err := s.store.ListClassrooms(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeList(w, cs)
}

func (s *Server) handleListAssignments(w http.ResponseWriter, r *http.Request) {
	cls, ok := s.requireClassroom(w, r)
	if !ok {
		return
	}
	as, err := s.store.ListAssignmentsByClassroom(r.Context(), cls.ID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeList(w, as)
}

func (s *Server) handleListRoster(w http.ResponseWriter, r *http.Request) {
	cls, ok := s.requireClassroom(w, r)
	if !ok {
		return
	}
	rs, err := s.store.ListRosterEntries(r.Context(), cls.ID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeList(w, rs)
}

// submissionView is the dashboard-facing projection of a submission: it joins the
// student's username and latest score onto the submission so the UI needs one call.
type submissionView struct {
	ID            string          `json:"id"`
	RosterEntryID string          `json:"roster_entry_id"`
	Username      string          `json:"username"`
	Repo          adapter.RepoRef `json:"repo"`
	Status        string          `json:"status"`
	LastError     string          `json:"last_error,omitempty"`
	Score         *float64        `json:"score,omitempty"`
	MaxScore      *float64        `json:"max_score,omitempty"`
	GradedAt      *time.Time      `json:"graded_at,omitempty"`
}

func (s *Server) handleListSubmissions(w http.ResponseWriter, r *http.Request) {
	asg, err := s.store.GetAssignment(r.Context(), r.PathValue("id"))
	if err != nil {
		s.notFoundOr500(w, err, "assignment")
		return
	}
	subs, err := s.store.ListSubmissionsByAssignment(r.Context(), asg.ID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]submissionView, 0, len(subs))
	for _, sub := range subs {
		v := submissionView{ID: sub.ID, RosterEntryID: sub.RosterEntryID, Repo: sub.Repo, Status: sub.Status, LastError: sub.LastError}
		if re, err := s.store.GetRosterEntry(r.Context(), sub.RosterEntryID); err == nil {
			v.Username = re.HostUsername
		}
		if g, err := s.store.LatestGradeForSubmission(r.Context(), sub.ID); err == nil {
			score, max, at := g.Score, g.MaxScore, g.GradedAt
			v.Score, v.MaxScore, v.GradedAt = &score, &max, &at
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleCreateClassroom(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string `json:"name"`
		Host          string `json:"host"`
		HostNamespace string `json:"host_namespace"`
		JoinPolicy    string `json:"join_policy"` // "open" (default) or "roster"
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Name == "" || in.Host == "" || in.HostNamespace == "" {
		httpError(w, http.StatusBadRequest, "name, host, and host_namespace are required")
		return
	}
	if !validSlug(in.HostNamespace) {
		httpError(w, http.StatusBadRequest, "host_namespace must be non-empty, ≤100 chars, contain only [A-Za-z0-9._-], and not start with '-' or '.'")
		return
	}
	if in.JoinPolicy == "" {
		in.JoinPolicy = store.ClassroomJoinPolicyOpen
	}
	if in.JoinPolicy != store.ClassroomJoinPolicyOpen && in.JoinPolicy != store.ClassroomJoinPolicyRoster {
		httpError(w, http.StatusBadRequest, `join_policy must be "open" or "roster"`)
		return
	}
	// Reject unknown hosts at create time so errors surface immediately rather
	// than three layers deep on first provisioning attempt. The allowlist is the
	// set of hosts with a registered adapter (what determines whether Cairn can
	// provision), so it stays correct automatically as hosts are added.
	if len(s.provisionHosts) > 0 {
		if !s.provisionHosts[adapter.Host(in.Host)] {
			valid := make([]string, 0, len(s.provisionHosts))
			for h := range s.provisionHosts {
				valid = append(valid, string(h))
			}
			sort.Strings(valid) // deterministic message
			httpError(w, http.StatusBadRequest, fmt.Sprintf("unknown host %q — valid hosts: %s", in.Host, strings.Join(valid, ", ")))
			return
		}
	}
	createdBy := ""
	if op, ok := operatorFrom(r.Context()); ok {
		createdBy = op.ID // empty in open/dev mode -> NULL created_by
	}
	c := &store.Classroom{
		ID:            id.New(),
		Name:          in.Name,
		Host:          adapter.Host(in.Host),
		HostNamespace: in.HostNamespace,
		JoinPolicy:    in.JoinPolicy,
		CreatedBy:     createdBy,
		CreatedAt:     time.Now(),
	}
	if err := s.store.CreateClassroom(r.Context(), c); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleAddRoster(w http.ResponseWriter, r *http.Request) {
	cls, ok := s.requireClassroom(w, r)
	if !ok {
		return
	}
	var in struct {
		Username  string `json:"username"`
		EmailHash string `json:"email_hash"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Username == "" {
		httpError(w, http.StatusBadRequest, "username is required")
		return
	}
	if !validSlug(in.Username) {
		httpError(w, http.StatusBadRequest, "username must be non-empty, ≤100 chars, contain only [A-Za-z0-9._-], and not start with '-' or '.'")
		return
	}
	re := &store.RosterEntry{
		ID:           id.New(),
		ClassroomID:  cls.ID,
		Host:         cls.Host,
		HostUsername: in.Username,
		EmailHash:    in.EmailHash,
		Status:       store.RosterInvited,
	}
	if err := s.store.CreateRosterEntry(r.Context(), re); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, re)
}

func (s *Server) handleCreateAssignment(w http.ResponseWriter, r *http.Request) {
	cls, ok := s.requireClassroom(w, r)
	if !ok {
		return
	}
	var in struct {
		Title    string `json:"title"`
		Slug     string `json:"slug"`
		Template struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
			Ref       string `json:"ref"`
		} `json:"template"`
		Type        string `json:"type"`
		GradingSpec string `json:"grading_spec"`
		Deadline    string `json:"deadline"` // optional RFC3339
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Title == "" || in.Slug == "" || in.Template.Namespace == "" || in.Template.Name == "" {
		httpError(w, http.StatusBadRequest, "title, slug, and template.namespace/template.name are required")
		return
	}
	if !validSlug(in.Slug) {
		httpError(w, http.StatusBadRequest, "slug must be non-empty, ≤100 chars, contain only [A-Za-z0-9._-], and not start with '-' or '.'")
		return
	}
	if !validSlug(in.Template.Namespace) {
		httpError(w, http.StatusBadRequest, "template.namespace must be non-empty, ≤100 chars, contain only [A-Za-z0-9._-], and not start with '-' or '.'")
		return
	}
	if !validSlug(in.Template.Name) {
		httpError(w, http.StatusBadRequest, "template.name must be non-empty, ≤100 chars, contain only [A-Za-z0-9._-], and not start with '-' or '.'")
		return
	}
	deadline, ok := parseDeadline(w, in.Deadline)
	if !ok {
		return
	}
	atype := store.AssignmentIndividual
	if in.Type == string(store.AssignmentGroup) {
		atype = store.AssignmentGroup
	}
	spec := in.GradingSpec
	if spec == "" {
		spec = "grading.json"
	}
	a := &store.Assignment{
		ID:          id.New(),
		ClassroomID: cls.ID,
		Title:       in.Title,
		Slug:        in.Slug,
		TemplateRef: adapter.TemplateRef{Host: cls.Host, Namespace: in.Template.Namespace, Name: in.Template.Name, Ref: in.Template.Ref},
		Type:        atype,
		GradingSpec: spec,
		Deadline:    deadline,
		CreatedAt:   time.Now(),
	}
	if err := s.store.CreateAssignment(r.Context(), a); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleSetDeadline(w http.ResponseWriter, r *http.Request) {
	asg, err := s.store.GetAssignment(r.Context(), r.PathValue("id"))
	if err != nil {
		s.notFoundOr500(w, err, "assignment")
		return
	}
	var in struct {
		Deadline *string `json:"deadline"` // null or "" clears the deadline
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Deadline == nil {
		asg.Deadline = nil
	} else {
		d, ok := parseDeadline(w, *in.Deadline)
		if !ok {
			return
		}
		asg.Deadline = d
	}
	if err := s.store.UpdateAssignment(r.Context(), asg); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, asg)
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	s.enqueueAcrossSubmissions(w, r, provisioning.JobLockRepo, "lock", "locking")
}
func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	s.enqueueAcrossSubmissions(w, r, provisioning.JobUnlockRepo, "unlock", "unlocking")
}
func (s *Server) handleGrade(w http.ResponseWriter, r *http.Request) {
	if !s.graderConfigured {
		httpError(w, http.StatusConflict, "no grader configured — set CAIRN_GRADER=container (or local-exec-unsafe) and restart")
		return
	}
	s.enqueueAcrossSubmissions(w, r, provisioning.JobGrade, "grade", "grading")
}

// enqueueAcrossSubmissions enqueues a job of type jt for every provisioned repo
// of an assignment. It backs the manual lock/unlock/grade triggers. Each call
// uses a unique idempotency key, so repeated requests are honored — the
// underlying operations (LockRepo/UnlockRepo, and re-grading) are safe to repeat.
func (s *Server) enqueueAcrossSubmissions(w http.ResponseWriter, r *http.Request, jt provisioning.JobType, keyPrefix, gerund string) {
	asg, err := s.store.GetAssignment(r.Context(), r.PathValue("id"))
	if err != nil {
		s.notFoundOr500(w, err, "assignment")
		return
	}
	subs, err := s.store.ListSubmissionsByAssignment(r.Context(), asg.ID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, skipped := 0, 0
	for _, sub := range subs {
		if sub.Repo.Name == "" {
			skipped++
			continue
		}
		if err := s.queue.Enqueue(r.Context(), jt, sub.ID, keyPrefix+":"+sub.ID+":"+id.New()); err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		n++
	}
	status := gerund
	if n == 0 {
		status = "no_eligible_submissions"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":                status,
		"jobs_enqueued":         n,
		"skipped_unprovisioned": skipped,
	})
}

func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	assignmentID := r.PathValue("id")
	asg, err := s.store.GetAssignment(r.Context(), assignmentID)
	if err != nil {
		s.notFoundOr500(w, err, "assignment")
		return
	}
	cls, err := s.store.GetClassroom(r.Context(), asg.ClassroomID)
	if err != nil {
		s.notFoundOr500(w, err, "classroom")
		return
	}
	resolver, ok := s.resolvers[cls.Host]
	if !ok {
		httpError(w, http.StatusInternalServerError, "self-enrollment is not configured for this host")
		return
	}
	state := id.New()
	now := time.Now()
	s.stMu.Lock()
	s.pruneStatesLocked(now)
	s.states[state] = authFlow{kind: "claim", assignmentID: assignmentID, host: cls.Host, created: now}
	s.stMu.Unlock()
	http.Redirect(w, r, resolver.AuthorizeURL(state), http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	s.stMu.Lock()
	flow, ok := s.states[state]
	if ok {
		delete(s.states, state) // one-time use regardless of TTL outcome
		if time.Since(flow.created) > authFlowTTL {
			ok = false
		}
	}
	s.stMu.Unlock()
	if !ok {
		httpError(w, http.StatusBadRequest, "unknown or expired state")
		return
	}

	resolver, ok := s.resolvers[flow.host]
	if !ok {
		httpError(w, http.StatusInternalServerError, "no resolver configured for this flow's host")
		return
	}
	username, hostUserID, err := resolver.Resolve(r.Context(), code)
	if err != nil {
		httpError(w, http.StatusBadGateway, "could not resolve identity: "+err.Error())
		return
	}

	switch flow.kind {
	case "login":
		s.completeOperatorLogin(w, r, flow.host, username, hostUserID)
	case "student_login":
		// Returning student: no admin check, no provisioning — just a session.
		s.startStudentSession(w, flow.host, username)
		http.Redirect(w, r, "/me", http.StatusFound)
	default: // "claim"
		s.completeStudentClaim(w, r, flow.assignmentID, username)
	}
}

// completeOperatorLogin authorizes an operator against the allowlist, upserts
// their User (keyed on the stable numeric hostUserID, not the mutable username),
// and starts a session.
func (s *Server) completeOperatorLogin(w http.ResponseWriter, r *http.Request, host adapter.Host, username, hostUserID string) {
	if !s.admins[username] {
		httpError(w, http.StatusForbidden, "this account is not authorized to operate this instance")
		return
	}
	u, err := s.store.FindUserByHostUserID(r.Context(), host, hostUserID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		u = &store.User{ID: id.New(), Host: host, HostUserID: hostUserID, HostUsername: username}
		if err := s.store.CreateUser(r.Context(), u); err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case err != nil:
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	token := newSessionToken()
	now := time.Now()
	s.sessMu.Lock()
	s.pruneSessionsLocked(now)
	s.sessions[token] = session{userID: u.ID, username: u.HostUsername, host: host, created: now, isOperator: true}
	s.sessMu.Unlock()
	s.setSessionCookie(w, token)
	http.Redirect(w, r, "/", http.StatusFound)
}

// setSessionCookie writes the session cookie with the standard attributes shared
// by operator and student logins.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.cookieSecure,
	})
}

// startStudentSession mints a student session (no User row, not an operator) and
// sets the cookie. Students are identified solely by their (host, username).
func (s *Server) startStudentSession(w http.ResponseWriter, host adapter.Host, username string) {
	token := newSessionToken()
	now := time.Now()
	s.sessMu.Lock()
	s.pruneSessionsLocked(now)
	s.sessions[token] = session{username: username, host: host, created: now, isOperator: false}
	s.sessMu.Unlock()
	s.setSessionCookie(w, token)
}

// completeStudentClaim binds a student's username to a roster entry and
// provisions their repo. Only the username is ever stored.
func (s *Server) completeStudentClaim(w http.ResponseWriter, r *http.Request, assignmentID, username string) {
	asg, err := s.store.GetAssignment(r.Context(), assignmentID)
	if err != nil {
		s.notFoundOr500(w, err, "assignment")
		return
	}
	cls, err := s.store.GetClassroom(r.Context(), asg.ClassroomID)
	if err != nil {
		s.notFoundOr500(w, err, "classroom")
		return
	}

	now := time.Now()
	re, err := s.store.FindRosterEntryByUsername(r.Context(), cls.ID, username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if cls.JoinPolicy == store.ClassroomJoinPolicyRoster {
			// Roster-only: unknown students are rejected rather than auto-enrolled.
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":    "not on roster",
				"username": username,
			})
			return
		}
		// Open self-enrollment: bind a new active roster entry. Only the username is stored.
		re = &store.RosterEntry{
			ID:           id.New(),
			ClassroomID:  cls.ID,
			Host:         cls.Host,
			HostUsername: username,
			Status:       store.RosterActive,
			ClaimedAt:    &now,
		}
		if err := s.store.CreateRosterEntry(r.Context(), re); err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case err != nil:
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	default:
		re.Status = store.RosterActive
		re.ClaimedAt = &now
		if err := s.store.UpdateRosterEntry(r.Context(), re); err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// Provision a repo for this (assignment, student) once. CreateSubmission is an
	// atomic check-and-insert: ErrConflict means a concurrent claim already won, so
	// there is nothing to do (idempotent). This eliminates the FindSubmission→create
	// TOCTOU window that previously let two goroutines both create a submission and
	// enqueue two provisioning jobs with different idempotency keys.
	sub := &store.Submission{
		ID:            id.New(),
		AssignmentID:  asg.ID,
		RosterEntryID: re.ID,
		Status:        "provisioning",
	}
	switch err := s.store.CreateSubmission(r.Context(), sub); {
	case errors.Is(err, store.ErrConflict):
		// Already provisioned or a concurrent claim beat us — nothing to do.
	case err != nil:
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	default:
		if err := s.queue.Enqueue(r.Context(), provisioning.JobCreateRepo, sub.ID, "repo:"+sub.ID); err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// The accept flow is browser-driven: give the student a session keyed to their
	// (host, username) and land them on their own-work page.
	s.startStudentSession(w, cls.Host, username)
	http.Redirect(w, r, "/me", http.StatusFound)
}

func (s *Server) handleGradesCSV(w http.ResponseWriter, r *http.Request) {
	cls, ok := s.requireClassroom(w, r)
	if !ok {
		return
	}
	assignments, err := s.store.ListAssignmentsByClassroom(r.Context(), cls.ID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slugByID := map[string]string{}
	for _, a := range assignments {
		slugByID[a.ID] = a.Slug
	}
	subs, err := s.store.ListSubmissionsByClassroom(r.Context(), cls.ID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"grades.csv\"")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"username", "assignment", "score", "max_score"})
	for _, sub := range subs {
		re, err := s.store.GetRosterEntry(r.Context(), sub.RosterEntryID)
		username := ""
		if err == nil {
			username = re.HostUsername
		}
		score, max := "", ""
		if g, err := s.store.LatestGradeForSubmission(r.Context(), sub.ID); err == nil {
			score = strconv.FormatFloat(g.Score, 'g', -1, 64)
			max = strconv.FormatFloat(g.MaxScore, 'g', -1, 64)
		}
		_ = cw.Write([]string{username, slugByID[sub.AssignmentID], score, max})
	}
	cw.Flush()
}

// --- helpers ---

func (s *Server) requireClassroom(w http.ResponseWriter, r *http.Request) (*store.Classroom, bool) {
	cls, err := s.store.GetClassroom(r.Context(), r.PathValue("id"))
	if err != nil {
		s.notFoundOr500(w, err, "classroom")
		return nil, false
	}
	return cls, true
}

func (s *Server) notFoundOr500(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, what+" not found")
		return
	}
	httpError(w, http.StatusInternalServerError, err.Error())
}

// parseDeadline parses an optional RFC3339 timestamp. An empty string yields a
// nil deadline (no deadline). On a malformed value it writes a 400 and returns
// ok=false.
func parseDeadline(w http.ResponseWriter, s string) (*time.Time, bool) {
	if s == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		httpError(w, http.StatusBadRequest, "deadline must be RFC3339, e.g. 2025-12-01T23:59:00Z")
		return nil, false
	}
	return &t, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeList writes a JSON array, normalizing a nil slice to [] so clients never
// receive null.
func writeList[T any](w http.ResponseWriter, items []T) {
	if items == nil {
		items = []T{}
	}
	writeJSON(w, http.StatusOK, items)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		status := http.StatusBadRequest
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
		}
		httpError(w, status, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "status": fmt.Sprintf("%d", status)})
}
