// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/internal/provisioning"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

const testWebhookSecret = "s3cr3t"

func githubSig(body []byte) string {
	m := hmac.New(sha256.New, []byte(testWebhookSecret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func giteaSig(body []byte) string {
	m := hmac.New(sha256.New, []byte(testWebhookSecret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// newWebhookServer builds a server wired with github+gitea webhook secrets and a
// single seeded submission per host. queue and graderConfigured are caller-chosen.
func newWebhookServer(t *testing.T, queue provisioning.Queue, graderConfigured bool) (*Server, *memory.Store) {
	t.Helper()
	st := memory.New()
	srv := New(Options{
		Store: st,
		Queue: queue,
		WebhookSecrets: map[adapter.Host]string{
			adapter.HostGitHub: testWebhookSecret,
			adapter.HostGitea:  testWebhookSecret,
			adapter.HostGitLab: testWebhookSecret,
		},
		GraderConfigured: graderConfigured,
	})
	ctx := context.Background()
	// GitHub-hosted submission.
	_ = st.CreateSubmission(ctx, &store.Submission{
		ID: "sub-gh", AssignmentID: "a1", RosterEntryID: "r1", Status: "active",
		Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "cs-dept", Name: "hw1-alice"},
	})
	// Gitea-hosted submission.
	_ = st.CreateSubmission(ctx, &store.Submission{
		ID: "sub-gt", AssignmentID: "a2", RosterEntryID: "r2", Status: "active",
		Repo: adapter.RepoRef{Host: adapter.HostGitea, Namespace: "cs-gitea", Name: "hw1-bob"},
	})
	// GitLab-hosted submission.
	_ = st.CreateSubmission(ctx, &store.Submission{
		ID: "sub-gl", AssignmentID: "a3", RosterEntryID: "r3", Status: "active",
		Repo: adapter.RepoRef{Host: adapter.HostGitLab, Namespace: "cs101", Name: "hw1-carol"},
	})
	return srv, st
}

func githubPush(ns, name, sha string) string {
	return `{"after":"` + sha + `","repository":{"name":"` + name + `","owner":{"login":"` + ns + `"}}}`
}

func giteaPush(ns, name, sha string) string {
	return `{"after":"` + sha + `","repository":{"name":"` + name + `","owner":{"username":"` + ns + `"}}}`
}

// gitlabPush builds a GitLab push event: object_kind=push, checkout_sha as the
// head, and the repo in project.path_with_namespace.
func gitlabPush(ns, name, sha string) string {
	return `{"object_kind":"push","checkout_sha":"` + sha + `","project":{"name":"` + name + `","path_with_namespace":"` + ns + `/` + name + `"}}`
}

func TestWebhookGitHubValidEnqueues(t *testing.T) {
	q := &spyQueue{}
	srv, st := newWebhookServer(t, q, true)

	body := githubPush("cs-dept", "hw1-alice", "abc123")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", githubSig([]byte(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if len(q.jobs) != 1 {
		t.Fatalf("jobs enqueued = %d, want 1", len(q.jobs))
	}
	if q.jobs[0].Type != provisioning.JobGrade || q.jobs[0].Target != "sub-gh" {
		t.Errorf("job = %+v, want grade for sub-gh", q.jobs[0])
	}
	if q.jobs[0].Idem != "grade:webhook:sub-gh:abc123" {
		t.Errorf("idem = %q, want grade:webhook:sub-gh:abc123", q.jobs[0].Idem)
	}
	// The push must be recorded on the submission.
	sub, _ := st.GetSubmission(context.Background(), "sub-gh")
	if sub.LatestCommit != "abc123" {
		t.Errorf("LatestCommit = %q, want abc123", sub.LatestCommit)
	}
	if sub.LastActivityAt == nil {
		t.Error("LastActivityAt should be set after a push")
	}
}

func TestWebhookGiteaValidEnqueues(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	body := giteaPush("cs-gitea", "hw1-bob", "def456")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitea", strings.NewReader(body))
	req.Header.Set("X-Gitea-Signature", giteaSig([]byte(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if len(q.jobs) != 1 || q.jobs[0].Target != "sub-gt" {
		t.Fatalf("jobs = %+v, want one for sub-gt", q.jobs)
	}
}

func TestWebhookForgejoSignatureHeaderAccepted(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	// A Forgejo instance may send X-Forgejo-Signature instead of X-Gitea-Signature.
	body := giteaPush("cs-gitea", "hw1-bob", "f00")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitea", strings.NewReader(body))
	req.Header.Set("X-Forgejo-Signature", giteaSig([]byte(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if len(q.jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(q.jobs))
	}
}

func TestWebhookBadSignature401(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	body := githubPush("cs-dept", "hw1-alice", "abc123")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString([]byte("wrong-digest-bytes-padding-xxxxx")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature status = %d, want 401", rec.Code)
	}
	if len(q.jobs) != 0 {
		t.Errorf("no jobs should be enqueued on bad signature, got %d", len(q.jobs))
	}
}

func TestWebhookMissingSignature401(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	body := githubPush("cs-dept", "hw1-alice", "abc123")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature status = %d, want 401", rec.Code)
	}
}

func TestWebhookUnknownRepo204(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	body := githubPush("cs-dept", "not-a-tracked-repo", "abc123")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", githubSig([]byte(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unknown repo status = %d, want 204", rec.Code)
	}
	if len(q.jobs) != 0 {
		t.Errorf("no jobs for an untracked repo, got %d", len(q.jobs))
	}
}

func TestWebhookNoSecretForHost404(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true) // github+gitea+gitlab secrets configured

	// An unconfigured host (no secret) cannot be trusted → 404.
	body := githubPush("x", "y", "z")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/bitbucket", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("no-secret host status = %d, want 404", rec.Code)
	}
}

func TestWebhookNoGrader200NoEnqueue(t *testing.T) {
	q := &spyQueue{}
	srv, st := newWebhookServer(t, q, false) // grader NOT configured

	body := githubPush("cs-dept", "hw1-alice", "abc999")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", githubSig([]byte(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("no-grader status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(q.jobs) != 0 {
		t.Errorf("no grader → no jobs, got %d", len(q.jobs))
	}
	// The push is still recorded even without a grader.
	sub, _ := st.GetSubmission(context.Background(), "sub-gh")
	if sub.LatestCommit != "abc999" {
		t.Errorf("LatestCommit = %q, want abc999 (push recorded without grader)", sub.LatestCommit)
	}
}

// TestWebhookSameShaDedupes uses the real, store-backed queue to prove that two
// deliveries of the same head commit enqueue exactly one grade job.
func TestWebhookSameShaDedupes(t *testing.T) {
	st := memory.New()
	queue := provisioning.NewService(st)
	srv := New(Options{
		Store:            st,
		Queue:            queue,
		WebhookSecrets:   map[adapter.Host]string{adapter.HostGitHub: testWebhookSecret},
		GraderConfigured: true,
	})
	ctx := context.Background()
	_ = st.CreateSubmission(ctx, &store.Submission{
		ID: "sub-gh", AssignmentID: "a1", RosterEntryID: "r1", Status: "active",
		Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "cs-dept", Name: "hw1-alice"},
	})

	body := githubPush("cs-dept", "hw1-alice", "samesha")
	deliver := func() int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", githubSig([]byte(body)))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := deliver(); c != http.StatusAccepted {
		t.Fatalf("first delivery = %d, want 202", c)
	}
	if c := deliver(); c != http.StatusAccepted {
		t.Fatalf("second delivery = %d, want 202", c)
	}

	// Exactly one grade job exists: first claim succeeds, second finds nothing.
	if _, err := st.ClaimNextJob(ctx); err != nil {
		t.Fatalf("first ClaimNextJob: %v, want one job", err)
	}
	if _, err := st.ClaimNextJob(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second ClaimNextJob: %v, want ErrNotFound (deduped)", err)
	}
}

// --- GitLab (token auth, not HMAC) ---------------------------------------

func TestWebhookGitLabValidEnqueues(t *testing.T) {
	q := &spyQueue{}
	srv, st := newWebhookServer(t, q, true)

	body := gitlabPush("cs101", "hw1-carol", "g1t1ab5ha")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	req.Header.Set("X-Gitlab-Token", testWebhookSecret) // verbatim secret, not an HMAC
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if len(q.jobs) != 1 || q.jobs[0].Target != "sub-gl" {
		t.Fatalf("jobs = %+v, want one for sub-gl", q.jobs)
	}
	if q.jobs[0].Idem != "grade:webhook:sub-gl:g1t1ab5ha" {
		t.Errorf("idem = %q", q.jobs[0].Idem)
	}
	sub, _ := st.GetSubmission(context.Background(), "sub-gl")
	if sub.LatestCommit != "g1t1ab5ha" {
		t.Errorf("LatestCommit = %q, want g1t1ab5ha", sub.LatestCommit)
	}
}

func TestWebhookGitLabWrongToken401(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	body := gitlabPush("cs101", "hw1-carol", "g1t1ab5ha")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "not-the-secret")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", rec.Code)
	}
	if len(q.jobs) != 0 {
		t.Errorf("no jobs on wrong token, got %d", len(q.jobs))
	}
}

func TestWebhookGitLabMissingToken401(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	body := gitlabPush("cs101", "hw1-carol", "g1t1ab5ha")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", rec.Code)
	}
}

func TestWebhookGitLabUnknownRepo204(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	body := gitlabPush("cs101", "not-tracked", "g1t1ab5ha")
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	req.Header.Set("X-Gitlab-Token", testWebhookSecret)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("unknown repo status = %d, want 204", rec.Code)
	}
	if len(q.jobs) != 0 {
		t.Errorf("no jobs for untracked repo, got %d", len(q.jobs))
	}
}

func TestWebhookGitLabNonPush204(t *testing.T) {
	q := &spyQueue{}
	srv, _ := newWebhookServer(t, q, true)

	// A tag_push event (valid token) must be ignored with 204.
	body := `{"object_kind":"tag_push","checkout_sha":"g1t1ab5ha","project":{"name":"hw1-carol","path_with_namespace":"cs101/hw1-carol"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab", strings.NewReader(body))
	req.Header.Set("X-Gitlab-Token", testWebhookSecret)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("tag_push status = %d, want 204", rec.Code)
	}
	if len(q.jobs) != 0 {
		t.Errorf("no jobs for non-push event, got %d", len(q.jobs))
	}
}
