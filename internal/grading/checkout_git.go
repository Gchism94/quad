// SPDX-License-Identifier: AGPL-3.0-or-later

package grading

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/quad/quad/pkg/adapter"
)

// CloneCreds holds the clone endpoint and credentials for one Git host.
// The token is optional; when empty, repos are cloned without authentication
// (public repos only). When set, it is delivered to git exclusively via
// GIT_ASKPASS — never in the URL or process arguments.
type CloneCreds struct {
	Scheme   string // "https" (default) or "http"; follows the host's base URL
	Hostname string // clone endpoint, e.g. "github.com" or "localhost:3000"
	Username string // URL username (non-secret), e.g. "x-access-token" or "oauth2"
	Token    string // optional; delivered via askpass, never in argv or .git/config
}

// GitCheckout fetches a submission repo with a shallow `git clone`.
//
// Per-host credentials are looked up from the Hosts map by repo.Host. An
// unknown host returns an error immediately — there is no implicit fallback.
//
// Credential hygiene (H1): the token is injected via GIT_ASKPASS and the
// $QUAD_GIT_TOKEN environment variable. It never appears in process arguments,
// the clone URL, or .git/config, so it cannot be read by untrusted student code
// in the grading container (which mounts the checkout read-write as /work).
type GitCheckout struct {
	Hosts  map[adapter.Host]CloneCreds
	GitBin string // default "git"
}

// NewGitCheckout returns a GitCheckout configured with the provided per-host
// credentials map. A host with an empty Token clones public repos without auth.
func NewGitCheckout(hosts map[adapter.Host]CloneCreds) *GitCheckout {
	return &GitCheckout{Hosts: hosts}
}

func (g *GitCheckout) bin() string {
	if g.GitBin != "" {
		return g.GitBin
	}
	return "git"
}

// cloneRequest computes the clone URL and the token for repo. The token is
// NEVER embedded in the URL; only the non-secret username is included when
// authentication is required. The scheme follows the host's configured base URL
// (CloneCreds.Scheme), defaulting to https — so a plain-http instance (e.g.
// http://localhost:3000) is cloned over http rather than forced through TLS.
// This function is pure (no exec), making it independently testable.
func (g *GitCheckout) cloneRequest(repo adapter.RepoRef) (cloneURL, token string, err error) {
	cc, ok := g.Hosts[repo.Host]
	if !ok || cc.Hostname == "" {
		return "", "", fmt.Errorf("checkout: no clone configuration for host %q", repo.Host)
	}
	scheme := cc.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if cc.Token != "" {
		user := cc.Username
		if user == "" {
			user = "x-access-token"
		}
		return fmt.Sprintf("%s://%s@%s/%s/%s.git", scheme, user, cc.Hostname, repo.Namespace, repo.Name),
			cc.Token, nil
	}
	return fmt.Sprintf("%s://%s/%s/%s.git", scheme, cc.Hostname, repo.Namespace, repo.Name), "", nil
}

// Fetch shallow-clones repo into dir (which must be an existing empty directory).
func (g *GitCheckout) Fetch(ctx context.Context, repo adapter.RepoRef, dir string) error {
	cloneURL, token, err := g.cloneRequest(repo)
	if err != nil {
		return err
	}

	args := []string{"clone", "--depth", "1"}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	if token != "" {
		// Write a temporary askpass helper to a scratch directory that is separate
		// from the checkout directory so it is never bind-mounted into the
		// grading container alongside the student submission.
		scratch, err := os.MkdirTemp("", "quad-askpass-*")
		if err != nil {
			return fmt.Errorf("askpass scratch: %w", err)
		}
		defer os.RemoveAll(scratch)

		askpass := filepath.Join(scratch, "askpass.sh")
		// The helper is invoked by git with the credential prompt as its first
		// argument. It echoes the token regardless of the prompt, which is safe
		// because git only calls GIT_ASKPASS for the password field.
		const script = "#!/bin/sh\nexec printf '%s\\n' \"$QUAD_GIT_TOKEN\"\n"
		if err := os.WriteFile(askpass, []byte(script), 0o700); err != nil {
			return fmt.Errorf("write askpass: %w", err)
		}

		env = append(env, "GIT_ASKPASS="+askpass, "QUAD_GIT_TOKEN="+token)
	}

	args = append(args, cloneURL, dir)
	cmd := exec.CommandContext(ctx, g.bin(), args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone: %v: %s", err, truncate(string(out)))
	}
	return nil
}
