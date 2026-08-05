// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/provisioning"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
)

// pushPayload is the subset of a host push event Cairn needs across all hosts.
// GitHub/Gitea/Forgejo use repository.{name, owner.login|username} + after;
// GitLab uses project.{name, path_with_namespace} + checkout_sha and marks the
// event type in object_kind.
type pushPayload struct {
	ObjectKind  string `json:"object_kind"`  // GitLab: "push" / "tag_push" / …
	After       string `json:"after"`        // GitHub/Gitea/Forgejo head sha
	CheckoutSHA string `json:"checkout_sha"` // GitLab head sha
	Repository  struct {
		Name  string `json:"name"`
		Owner struct {
			Login    string `json:"login"`
			Username string `json:"username"`
		} `json:"owner"`
	} `json:"repository"`
	Project struct {
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
		Namespace         string `json:"namespace"`
	} `json:"project"`
}

// repo extracts (namespace, name, headSHA) for the given host. The fields a host
// populates differ, so the host selects which to read.
func (p pushPayload) repo(host adapter.Host) (namespace, name, sha string) {
	if host == adapter.HostGitLab {
		ns, base := splitLastSlash(p.Project.PathWithNamespace)
		if base == "" {
			base = p.Project.Name
		}
		if ns == "" {
			ns = p.Project.Namespace
		}
		return ns, base, p.CheckoutSHA
	}
	ns := p.Repository.Owner.Login
	if ns == "" {
		ns = p.Repository.Owner.Username
	}
	return ns, p.Repository.Name, p.After
}

// splitLastSlash splits "group/project" (or "group/sub/project") into the
// namespace (everything before the last "/") and the final path segment.
func splitLastSlash(path string) (dir, base string) {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return "", path
	}
	return path[:i], path[i+1:]
}

// handleWebhook receives a host push delivery, authenticates it against the
// per-host secret (HMAC for GitHub/Gitea/Forgejo; the X-Gitlab-Token header for
// GitLab), maps the repo to a submission, and (if a grader is configured) enqueues
// a regrade keyed to the head commit so each push grades once.
//
// It is public by necessity — the Git host calls it — so the secret is the only
// trust boundary. No secret configured for a host means we cannot trust any
// delivery for it, so we 404.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	host := adapter.Host(r.PathValue("host"))
	secret := s.webhookSecrets[host]
	if secret == "" {
		// No secret → unverifiable → we do not accept deliveries for this host.
		httpError(w, http.StatusNotFound, "no webhook secret configured for this host")
		return
	}

	// Read the raw body once: HMAC must be computed over the exact bytes the host
	// signed, so we cannot let json.Decode consume the stream first.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	if !verifyWebhookSignature(host, r.Header, body, secret) {
		httpError(w, http.StatusUnauthorized, "invalid or missing signature")
		return
	}

	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		httpError(w, http.StatusBadRequest, "malformed payload")
		return
	}

	// GitLab marks the event type explicitly; ignore non-push events (tag_push,
	// issues, etc.). GitHub/Gitea/Forgejo non-push deliveries carry no head commit
	// and fall through to the sha check below.
	if host == adapter.HostGitLab && payload.ObjectKind != "push" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Non-push deliveries (e.g. ping) and branch deletions carry no actionable head
	// commit. Acknowledge them without doing anything.
	ns, name, sha := payload.repo(host)
	if name == "" || sha == "" || isZeroSHA(sha) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	sub, err := s.store.FindSubmissionByRepo(r.Context(), host, ns, name)
	if err != nil {
		// A delivery for a repo we don't track is not an error.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Record the new head so the student view reflects the push immediately.
	now := time.Now()
	sub.LatestCommit = sha
	sub.LastActivityAt = &now
	if err := s.store.UpdateSubmission(r.Context(), sub); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if !s.graderConfigured {
		// Don't enqueue jobs that would only fail; the push is still recorded.
		log.Printf("webhook: push for submission %s recorded; no grader configured, skipping regrade", sub.ID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Idempotency key includes the sha so the same push grades once but a new
	// commit triggers a fresh regrade.
	idem := "grade:webhook:" + sub.ID + ":" + sha
	if err := s.queue.Enqueue(r.Context(), provisioning.JobGrade, sub.ID, idem); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// verifyWebhookSignature authenticates a delivery per host's scheme:
//   - GitLab does NOT HMAC-sign; it echoes the configured secret verbatim in the
//     X-Gitlab-Token header. We compare it to the secret in constant time.
//   - GitHub sends X-Hub-Signature-256: sha256=<hex> (HMAC-SHA256 over the body).
//   - Gitea/Forgejo send X-Gitea-Signature / X-Forgejo-Signature: <hex> (same HMAC).
func verifyWebhookSignature(host adapter.Host, h http.Header, body []byte, secret string) bool {
	if host == adapter.HostGitLab {
		got := []byte(h.Get("X-Gitlab-Token"))
		return len(got) > 0 && hmac.Equal(got, []byte(secret))
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)

	var got []byte
	if host == adapter.HostGitHub {
		sig := h.Get("X-Hub-Signature-256")
		hexPart, ok := strings.CutPrefix(sig, "sha256=")
		if !ok {
			return false
		}
		got = decodeHex(hexPart)
	} else {
		sig := h.Get("X-Gitea-Signature")
		if sig == "" {
			sig = h.Get("X-Forgejo-Signature")
		}
		got = decodeHex(sig)
	}
	if len(got) == 0 {
		return false
	}
	return hmac.Equal(got, want)
}

func decodeHex(s string) []byte {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return b
}

// isZeroSHA reports whether sha is the all-zero SHA a host sends for a branch
// deletion (no head commit to grade).
func isZeroSHA(sha string) bool {
	for _, c := range sha {
		if c != '0' {
			return false
		}
	}
	return true
}
