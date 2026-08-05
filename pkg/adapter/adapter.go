// SPDX-License-Identifier: Apache-2.0

// Package adapter defines the host-agnostic interface that every Git-host
// integration implements. It is the load-bearing seam that lets Cairn work
// against GitHub, GitLab, and self-hosted Forgejo/Gitea without the rest of the
// system knowing which host it is talking to.
//
// This package is intentionally dependency-free (standard library only) and
// permissively licensed (Apache-2.0, distinct from the AGPL control plane) so
// it can be reused and re-implemented independently.
package adapter

import (
	"context"
	"errors"
	"time"
)

// Host identifies a supported Git host.
type Host string

const (
	HostGitHub  Host = "github"
	HostGitLab  Host = "gitlab"
	HostForgejo Host = "forgejo" // Gitea-family; see HostGitea
	HostGitea   Host = "gitea"   // same /api/v1 surface as Forgejo; one adapter family
)

// Role is a collaborator's access level, normalized across hosts.
type Role string

const (
	RoleRead  Role = "read"
	RoleWrite Role = "write"
	RoleAdmin Role = "admin"
)

// NamespaceRef identifies an org (GitHub/Forgejo) or group (GitLab) under which
// classroom repositories live.
type NamespaceRef struct {
	Host Host   `json:"host"`
	Slug string `json:"slug"` // org/group identifier on the host, e.g. "cs101-fall25"
}

// RepoRef identifies a single repository on a host.
type RepoRef struct {
	Host      Host   `json:"host"`
	Namespace string `json:"namespace"` // org/group slug
	Name      string `json:"name"`      // repository name
}

// TemplateRef identifies the template repository an assignment is generated from.
type TemplateRef struct {
	Host      Host   `json:"host"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ref       string `json:"ref,omitempty"` // optional branch/tag/SHA; empty means the default branch
}

// Commit is a normalized view of a single commit.
type Commit struct {
	SHA            string    `json:"sha"`
	Message        string    `json:"message"`
	Timestamp      time.Time `json:"timestamp"`
	AuthorUsername string    `json:"author_username,omitempty"` // host username when resolvable, else empty
}

// CreateRepoOptions controls repository creation.
type CreateRepoOptions struct {
	Private            bool
	IncludeAllBranches bool // template every branch, not just the default
	Description        string
}

// WebhookSpec describes a webhook to ensure on a repository. Webhooks are the
// primary mechanism for learning about student pushes; pollers are the fallback.
type WebhookSpec struct {
	URL    string
	Secret string
	Events []string // host-neutral event names; adapters translate them
}

// GradingDispatch requests a grading run for a repo at a commit. Adapters may
// satisfy this by triggering host-native CI, or may return ErrNotImplemented to
// signal that the caller should grade on Cairn's own sandboxed runners.
type GradingDispatch struct {
	Repo     RepoRef
	SHA      string
	SpecPath string // path to the portable grading spec inside the repo
}

// CheckStatus is the normalized state of a grading/CI run.
type CheckStatus string

const (
	CheckPending CheckStatus = "pending"
	CheckRunning CheckStatus = "running"
	CheckPassed  CheckStatus = "passed"
	CheckFailed  CheckStatus = "failed"
	CheckError   CheckStatus = "error"
)

// CheckResult is a grading/CI result read back from the host.
type CheckResult struct {
	Status    CheckStatus `json:"status"`
	Score     *float64    `json:"score,omitempty"` // nil when the run reports no numeric score
	MaxScore  *float64    `json:"max_score,omitempty"`
	DetailURL string      `json:"detail_url,omitempty"`
}

// ErrNotImplemented is returned for capabilities an adapter does not (yet)
// support. Callers should treat it as a soft failure where reasonable.
var ErrNotImplemented = errors.New("adapter: not implemented")

// Adapter is the contract every Git-host integration implements. v1 ships a
// GitHub adapter; GitLab and Forgejo/Gitea are additive implementations of this
// same interface.
//
// Implementations MUST be safe for concurrent use: the provisioning queue calls
// these methods from many workers at once. Each method must be individually
// retry-safe, and verbs that create state (EnsureNamespace, CreateRepoFromTemplate,
// SetCollaborator, EnsureWebhook) must be idempotent — a repeated call that
// reaches the desired state must not error.
type Adapter interface {
	// Host returns the host this adapter targets.
	Host() Host

	// RepoWebURL returns the browser-facing URL of a repository on this host, used
	// to give students a clickable link to their own repo. It is a pure string
	// builder (no network call).
	RepoWebURL(repo RepoRef) string

	// EnsureNamespace makes sure the org/group exists, creating it if needed.
	EnsureNamespace(ctx context.Context, slug string) (NamespaceRef, error)

	// CreateRepoFromTemplate creates or reconciles a repo from a template. A
	// second call for an already-created repo returns the existing ref, no error.
	CreateRepoFromTemplate(ctx context.Context, tmpl TemplateRef, ns NamespaceRef, name string, opts CreateRepoOptions) (RepoRef, error)

	// RepoExists reports whether the repo exists.
	RepoExists(ctx context.Context, repo RepoRef) (bool, error)

	// SetCollaborator grants username the given role on repo.
	SetCollaborator(ctx context.Context, repo RepoRef, username string, role Role) error

	// RemoveCollaborator revokes username's access to repo.
	RemoveCollaborator(ctx context.Context, repo RepoRef, username string) error

	// LatestCommit returns the most recent commit on a branch (empty branch =
	// default branch). Returns a zero Commit and nil error if there are none yet.
	LatestCommit(ctx context.Context, repo RepoRef, branch string) (Commit, error)

	// LockRepo restricts pushes, used to enforce a deadline.
	LockRepo(ctx context.Context, repo RepoRef) error

	// UnlockRepo restores push access.
	UnlockRepo(ctx context.Context, repo RepoRef) error

	// EnsureWebhook makes sure a webhook matching spec exists on repo.
	EnsureWebhook(ctx context.Context, repo RepoRef, spec WebhookSpec) error

	// DispatchGrading requests a grading run. Returning ErrNotImplemented signals
	// the orchestrator to run grading on Cairn's own runners instead.
	DispatchGrading(ctx context.Context, d GradingDispatch) error

	// GradingResult reads back a grading/CI result for repo at sha.
	GradingResult(ctx context.Context, repo RepoRef, sha string) (CheckResult, error)
}
