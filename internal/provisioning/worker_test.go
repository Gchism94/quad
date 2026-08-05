// SPDX-License-Identifier: AGPL-3.0-or-later

package provisioning

import (
	"context"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/internal/store/memory"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// fakeAdapter records the calls the worker makes. Unused methods are no-ops.
type fakeAdapter struct {
	callOrder    []string // ordered list of method names called
	ensuredSlug  string
	createdRepo  string
	collaborator string
	role         adapter.Role
	webhookURL   string
	lockedRepo   string
	unlockedRepo string
}

func (f *fakeAdapter) Host() adapter.Host { return adapter.HostGitHub }
func (f *fakeAdapter) RepoWebURL(repo adapter.RepoRef) string {
	return "https://github.test/" + repo.Namespace + "/" + repo.Name
}

func (f *fakeAdapter) EnsureNamespace(_ context.Context, slug string) (adapter.NamespaceRef, error) {
	f.callOrder = append(f.callOrder, "EnsureNamespace")
	f.ensuredSlug = slug
	return adapter.NamespaceRef{Host: adapter.HostGitHub, Slug: slug}, nil
}

func (f *fakeAdapter) CreateRepoFromTemplate(_ context.Context, _ adapter.TemplateRef, ns adapter.NamespaceRef, name string, _ adapter.CreateRepoOptions) (adapter.RepoRef, error) {
	f.callOrder = append(f.callOrder, "CreateRepoFromTemplate")
	f.createdRepo = name
	return adapter.RepoRef{Host: adapter.HostGitHub, Namespace: ns.Slug, Name: name}, nil
}

func (f *fakeAdapter) RepoExists(context.Context, adapter.RepoRef) (bool, error) { return false, nil }

func (f *fakeAdapter) SetCollaborator(_ context.Context, _ adapter.RepoRef, username string, role adapter.Role) error {
	f.collaborator = username
	f.role = role
	return nil
}

func (f *fakeAdapter) RemoveCollaborator(context.Context, adapter.RepoRef, string) error { return nil }

func (f *fakeAdapter) LatestCommit(context.Context, adapter.RepoRef, string) (adapter.Commit, error) {
	return adapter.Commit{}, nil
}

func (f *fakeAdapter) LockRepo(_ context.Context, repo adapter.RepoRef) error {
	f.lockedRepo = repo.Name
	return nil
}
func (f *fakeAdapter) UnlockRepo(_ context.Context, repo adapter.RepoRef) error {
	f.unlockedRepo = repo.Name
	return nil
}

func (f *fakeAdapter) EnsureWebhook(_ context.Context, _ adapter.RepoRef, spec adapter.WebhookSpec) error {
	f.webhookURL = spec.URL
	return nil
}

func (f *fakeAdapter) DispatchGrading(context.Context, adapter.GradingDispatch) error { return nil }

func (f *fakeAdapter) GradingResult(context.Context, adapter.RepoRef, string) (adapter.CheckResult, error) {
	return adapter.CheckResult{}, nil
}

func TestWorkerCreateRepo(t *testing.T) {
	ctx := context.Background()
	st := memory.New()

	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "cs101-org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "cs101-org", Name: "hw1-template"},
	})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "bob"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1", Status: "provisioning"})

	queue := NewService(st)
	if err := queue.Enqueue(ctx, JobCreateRepo, "s1", "repo:s1"); err != nil {
		t.Fatal(err)
	}

	fa := &fakeAdapter{}
	w := &Worker{
		Store:          st,
		Adapters:       map[adapter.Host]adapter.Adapter{adapter.HostGitHub: fa},
		WebhookBaseURL: "https://cairn.example",
	}
	did, err := w.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !did {
		t.Fatal("expected a job to be claimed")
	}

	if fa.createdRepo != "hw1-bob" {
		t.Fatalf("createdRepo = %q, want hw1-bob", fa.createdRepo)
	}
	if fa.collaborator != "bob" || fa.role != adapter.RoleWrite {
		t.Fatalf("collaborator = %q role = %q, want bob/write", fa.collaborator, fa.role)
	}
	// The webhook URL is per-host: <base>/webhooks/<host>.
	if fa.webhookURL != "https://cairn.example/webhooks/github" {
		t.Fatalf("webhookURL = %q, want https://cairn.example/webhooks/github", fa.webhookURL)
	}

	sub, _ := st.GetSubmission(ctx, "s1")
	if sub.Repo.Name != "hw1-bob" || sub.Status != "active" {
		t.Fatalf("submission = %+v, want repo hw1-bob and status active", sub)
	}

	// Queue should now be drained.
	if did, _ := w.RunOnce(ctx); did {
		t.Fatal("expected no more jobs")
	}
}

// TestWorkerWebhookURLPerHost verifies the worker derives the webhook URL from the
// classroom's host, so each host's repos point at the handler that can verify them
// (a fix for the prior single-URL behaviour that broke multi-host deployments).
func TestWorkerWebhookURLPerHost(t *testing.T) {
	cases := []struct {
		host adapter.Host
		want string
	}{
		{adapter.HostGitHub, "https://cairn.example/webhooks/github"},
		{adapter.HostForgejo, "https://cairn.example/webhooks/forgejo"},
		{adapter.HostGitea, "https://cairn.example/webhooks/gitea"},
		{adapter.HostGitLab, "https://cairn.example/webhooks/gitlab"},
	}
	for _, tc := range cases {
		t.Run(string(tc.host), func(t *testing.T) {
			ctx := context.Background()
			st := memory.New()
			_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: tc.host, HostNamespace: "org"})
			_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1", TemplateRef: adapter.TemplateRef{Host: tc.host, Namespace: "org", Name: "tmpl"}})
			_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: tc.host, HostUsername: "bob"})
			_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1", Status: "provisioning"})

			queue := NewService(st)
			_ = queue.Enqueue(ctx, JobCreateRepo, "s1", "repo:s1")

			fa := &fakeAdapter{}
			// Trailing slash on the base also exercises the TrimRight.
			w := &Worker{Store: st, Adapters: map[adapter.Host]adapter.Adapter{tc.host: fa}, WebhookBaseURL: "https://cairn.example/"}
			if _, err := w.RunOnce(ctx); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if fa.webhookURL != tc.want {
				t.Fatalf("webhook URL = %q, want %q", fa.webhookURL, tc.want)
			}
		})
	}
}

func TestWorkerLockUnlock(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateSubmission(ctx, &store.Submission{
		ID:           "s1",
		AssignmentID: "a1",
		Status:       "active",
		Repo:         adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org", Name: "hw1-bob"},
	})
	fa := &fakeAdapter{}
	w := &Worker{Store: st, Adapters: map[adapter.Host]adapter.Adapter{adapter.HostGitHub: fa}}
	queue := NewService(st)

	// Lock.
	if err := queue.Enqueue(ctx, JobLockRepo, "s1", "lock:s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatalf("lock RunOnce: %v", err)
	}
	if fa.lockedRepo != "hw1-bob" {
		t.Fatalf("lockedRepo = %q, want hw1-bob", fa.lockedRepo)
	}
	if sub, _ := st.GetSubmission(ctx, "s1"); sub.Status != "locked" {
		t.Fatalf("status = %q, want locked", sub.Status)
	}

	// Unlock.
	if err := queue.Enqueue(ctx, JobUnlockRepo, "s1", "unlock:s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatalf("unlock RunOnce: %v", err)
	}
	if fa.unlockedRepo != "hw1-bob" {
		t.Fatalf("unlockedRepo = %q, want hw1-bob", fa.unlockedRepo)
	}
	if sub, _ := st.GetSubmission(ctx, "s1"); sub.Status != "active" {
		t.Fatalf("status = %q, want active", sub.Status)
	}
}

func TestSetLockNoRepoIsNoop(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", Status: "provisioning"})
	fa := &fakeAdapter{}
	w := &Worker{Store: st, Adapters: map[adapter.Host]adapter.Adapter{adapter.HostGitHub: fa}}
	if err := w.setLock(ctx, "s1", true); err != nil {
		t.Fatalf("setLock on unprovisioned submission should be a no-op, got %v", err)
	}
	if fa.lockedRepo != "" {
		t.Fatalf("expected no lock call, got %q", fa.lockedRepo)
	}
}

func TestWorkerNoAdapterFailsJob(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{ID: "a1", ClassroomID: "c1", Slug: "hw1"})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", HostUsername: "bob"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1"})

	queue := NewService(st)
	_ = queue.Enqueue(ctx, JobCreateRepo, "s1", "repo:s1")

	w := &Worker{Store: st, Adapters: map[adapter.Host]adapter.Adapter{}, MaxAttempts: 1}
	did, err := w.RunOnce(ctx)
	if !did {
		t.Fatal("expected a job to be claimed")
	}
	if err == nil {
		t.Fatal("expected an execution error when no adapter is configured")
	}
}

type fakeGrader struct{ graded []string }

func (g *fakeGrader) Grade(_ context.Context, submissionID string) error {
	g.graded = append(g.graded, submissionID)
	return nil
}

func TestWorkerGradeJobInvokesGrader(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org", Name: "hw1-bob"}})
	queue := NewService(st)
	_ = queue.Enqueue(ctx, JobGrade, "s1", "grade:s1")

	g := &fakeGrader{}
	w := &Worker{Store: st, Adapters: map[adapter.Host]adapter.Adapter{}, Grader: g}
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(g.graded) != 1 || g.graded[0] != "s1" {
		t.Fatalf("graded = %v, want [s1]", g.graded)
	}
}

func TestWorkerGradeJobWithoutGraderFails(t *testing.T) {
	ctx := context.Background()
	st := memory.New()
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", Repo: adapter.RepoRef{Host: adapter.HostGitHub, Namespace: "org", Name: "hw1-bob"}})
	queue := NewService(st)
	_ = queue.Enqueue(ctx, JobGrade, "s1", "grade:s1")

	w := &Worker{Store: st, Adapters: map[adapter.Host]adapter.Adapter{}} // no grader
	did, err := w.RunOnce(ctx)
	if !did {
		t.Fatal("expected a job to be claimed")
	}
	if err == nil {
		t.Fatal("expected an error when no grader is configured")
	}
}

// TestWorkerEnsureNamespaceCalledFirst asserts that EnsureNamespace is called
// before CreateRepoFromTemplate and that its returned ref is what flows through.
func TestWorkerEnsureNamespaceCalledFirst(t *testing.T) {
	ctx := context.Background()
	st := memory.New()

	_ = st.CreateClassroom(ctx, &store.Classroom{ID: "c1", Host: adapter.HostGitHub, HostNamespace: "cs101-org"})
	_ = st.CreateAssignment(ctx, &store.Assignment{
		ID: "a1", ClassroomID: "c1", Slug: "hw1",
		TemplateRef: adapter.TemplateRef{Host: adapter.HostGitHub, Namespace: "cs101-org", Name: "hw1-template"},
	})
	_ = st.CreateRosterEntry(ctx, &store.RosterEntry{ID: "r1", ClassroomID: "c1", Host: adapter.HostGitHub, HostUsername: "alice"})
	_ = st.CreateSubmission(ctx, &store.Submission{ID: "s1", AssignmentID: "a1", RosterEntryID: "r1", Status: "provisioning"})

	queue := NewService(st)
	_ = queue.Enqueue(ctx, JobCreateRepo, "s1", "repo:s1")

	fa := &fakeAdapter{}
	w := &Worker{Store: st, Adapters: map[adapter.Host]adapter.Adapter{adapter.HostGitHub: fa}}
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	// EnsureNamespace must come before CreateRepoFromTemplate.
	if len(fa.callOrder) < 2 {
		t.Fatalf("expected at least 2 calls, got %v", fa.callOrder)
	}
	if fa.callOrder[0] != "EnsureNamespace" {
		t.Errorf("first call = %q, want EnsureNamespace", fa.callOrder[0])
	}
	if fa.callOrder[1] != "CreateRepoFromTemplate" {
		t.Errorf("second call = %q, want CreateRepoFromTemplate", fa.callOrder[1])
	}
	if fa.ensuredSlug != "cs101-org" {
		t.Errorf("ensuredSlug = %q, want cs101-org", fa.ensuredSlug)
	}
	// The repo should be created under the slug returned by EnsureNamespace.
	sub, _ := st.GetSubmission(ctx, "s1")
	if sub.Repo.Namespace != "cs101-org" {
		t.Errorf("repo.Namespace = %q, want cs101-org", sub.Repo.Namespace)
	}
	if sub.Repo.Name != "hw1-alice" {
		t.Errorf("repo.Name = %q, want hw1-alice", sub.Repo.Name)
	}
}
