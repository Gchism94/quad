// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// --- store ----------------------------------------------------------------

// CheckStore reports whether the configured store is reachable. kind is the
// human-readable description from the server's own store selection (e.g.
// "sqlite /var/lib/cairn/cairn.db"), and ping performs a trivial read.
func CheckStore(ctx context.Context, kind string, ping func(context.Context) error) Result {
	const name = "store"
	if ping == nil {
		return failf(name, "check CAIRN_STORE / CAIRN_DATABASE_URL / CAIRN_SQLITE_PATH", "%s — could not be opened", kind)
	}
	if err := ping(ctx); err != nil {
		if looksUnmigrated(err) {
			return failf(name,
				"start Cairn once with CAIRN_DB_AUTOMIGRATE=1 to apply the schema (it is idempotent)",
				"%s — connected, but the schema is missing: %v", kind, err)
		}
		return failf(name,
			"confirm the database is running and CAIRN_DATABASE_URL is correct;\n"+
				"in Docker the host is the compose service name (e.g. postgres), not localhost",
			"%s — unreachable: %v", kind, err)
	}
	if strings.HasPrefix(kind, "memory") {
		return warnf(name,
			"set CAIRN_DATABASE_URL (PostgreSQL) or CAIRN_SQLITE_PATH for a durable store",
			"%s — every classroom, roster, and grade is lost on restart", kind)
	}
	return okf(name, "%s — reachable", kind)
}

// looksUnmigrated distinguishes "the database is fine but empty" from "the
// database is unreachable", because the fixes are completely different.
func looksUnmigrated(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "does not exist") || // postgres: relation "classrooms" does not exist
		strings.Contains(s, "no such table") || // sqlite
		strings.Contains(s, "undefined table")
}

// --- environment ----------------------------------------------------------

// CheckEnvNames flags CAIRN_* variables that Cairn does not read. This catches
// the most demoralizing class of misconfiguration: a variable that looks right,
// is spelled almost right, and is silently ignored.
//
// environ is os.Environ() form ("KEY=value"); known is every variable Cairn
// actually reads; ignored maps a recognized-but-unused variable to the reason it
// does nothing.
func CheckEnvNames(environ, known []string, ignored map[string]string) []Result {
	const name = "environment"
	knownSet := make(map[string]bool, len(known))
	for _, k := range known {
		knownSet[k] = true
	}

	var unknown, dead []string
	for _, kv := range environ {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, "CAIRN_") {
			continue
		}
		switch {
		case ignored[key] != "":
			dead = append(dead, key)
		case !knownSet[key]:
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	sort.Strings(dead)

	var out []Result
	for _, key := range unknown {
		fix := "remove it — Cairn never reads this name"
		if near := nearest(key, known); near != "" {
			fix = fmt.Sprintf("did you mean %s?", near)
		}
		out = append(out, warnf(name, fix, "%s is set but Cairn does not read it", key))
	}
	for _, key := range dead {
		out = append(out, warnf(name, ignored[key], "%s is set but has no effect", key))
	}
	if len(out) == 0 {
		return []Result{okf(name, "no unrecognized CAIRN_* variables")}
	}
	return out
}

// nearest returns the most likely intended name, or "".
//
// Truncation is the dominant real-world shape — CAIRN_LISTEN for
// CAIRN_LISTEN_ADDR — and it is far outside any sane edit-distance limit, so a
// prefix relationship wins outright. Typos fall back to edit distance.
func nearest(key string, known []string) string {
	best := ""
	for _, k := range known {
		if k == key || !strings.HasPrefix(k, key) && !strings.HasPrefix(key, k) {
			continue
		}
		// Prefer the closest-length relative, e.g. CAIRN_GITHUB_CLIENT_ID over
		// CAIRN_GITHUB_CLIENT_SECRET for CAIRN_GITHUB_CLIENT.
		if best == "" || len(k) < len(best) {
			best = k
		}
	}
	if best != "" {
		return best
	}

	bestD := 0
	limit := 1 + len(key)/5 // roughly one edit per five characters
	for _, k := range known {
		d := editDistance(key, k)
		if d <= limit && (best == "" || d < bestD) {
			best, bestD = k, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// --- listen address -------------------------------------------------------

// CheckListenAddr reports whether Cairn can serve on its listen address.
//
// bind should attempt a listen and immediately close. serving should report
// whether a healthy Cairn already answers there — without it, the check would
// fail on every healthy deployment, since the usual way to run doctor
// (`docker compose exec cairn cairn doctor`) shares a network namespace with the
// running server and can never bind the port it is already using.
func CheckListenAddr(addr string, bind func(string) error, serving func(string) bool) Result {
	const name = "listen address"
	err := bind(addr)
	if err == nil {
		return okf(name, "%s is available", addr)
	}
	if serving != nil && serving(addr) {
		return okf(name, "%s is already served by a healthy Cairn", addr)
	}
	return failf(name,
		"stop whatever holds the port, or set CAIRN_LISTEN_ADDR to a free one",
		"%s is held by another process and Cairn is not answering there: %v", addr, err)
}

// --- host adapters --------------------------------------------------------

// AdapterState is one Git host's configuration status, as determined by the
// caller (which owns the host-specific environment wiring).
type AdapterState struct {
	Host string
	// Configured is true when credentials are present.
	Configured bool
	// Err is a configuration problem, e.g. an unreadable private key.
	Err error
	// Detail describes what was found, e.g. "GitHub App 12345, installation 678".
	Detail string
	// ProbeErr is set when a live credential check ran and failed.
	ProbeErr error
	// Probed is true when a live check actually ran.
	Probed bool
}

// CheckAdapters reports on Git-host credentials. Without at least one adapter,
// Cairn can store classrooms but cannot create a single repository.
func CheckAdapters(states []AdapterState) []Result {
	const name = "host adapter"
	var out []Result
	configured := 0
	for _, s := range states {
		switch {
		case s.Err != nil:
			out = append(out, failf(name,
				fmt.Sprintf("fix the %s credentials in your environment (see docs/%s-setup.md)", s.Host, s.Host),
				"%s: %v", s.Host, s.Err))
		case !s.Configured:
			continue // an unconfigured host is a choice, not a problem
		case s.Probed && s.ProbeErr != nil:
			configured++
			out = append(out, failf(name,
				fmt.Sprintf("the %s credentials are present but rejected — re-check the token/App installation", s.Host),
				"%s: %s — live check failed: %v", s.Host, s.Detail, s.ProbeErr))
		case s.Probed:
			configured++
			out = append(out, okf(name, "%s: %s — credentials accepted", s.Host, s.Detail))
		default:
			configured++
			out = append(out, okf(name, "%s: %s (not verified against the host; use --verify-hosts)", s.Host, s.Detail))
		}
	}
	if configured == 0 {
		out = append(out, warnf(name,
			"configure at least one host: CAIRN_GITHUB_APP_ID…, CAIRN_FORGEJO_BASE_URL…, or CAIRN_GITLAB_TOKEN…\n"+
				"see docs/github-setup.md, docs/forgejo-setup.md, docs/gitlab-setup.md",
			"no Git host is configured — classrooms can be created, but no repository can be provisioned"))
	}
	return out
}

// --- webhooks -------------------------------------------------------------

// CheckWebhooks reports whether push deliveries can arrive and be verified.
// secrets maps host → whether a signing secret is set; hosts is the set of hosts
// with a configured adapter.
func CheckWebhooks(baseURL string, hosts []string, secrets map[string]bool, secretEnvFor func(string) string) []Result {
	const name = "webhooks"
	if len(hosts) == 0 {
		return nil // nothing to deliver to; CheckAdapters already said so
	}
	var out []Result

	switch {
	case baseURL == "":
		out = append(out, warnf(name,
			"set CAIRN_WEBHOOK_BASE_URL to this server's public URL, e.g. https://cairn.example.edu",
			"no webhook base URL — pushes will not trigger regrading"))
	case !strings.HasPrefix(baseURL, "https://"):
		fix := "use an https:// URL; Git hosts refuse or downgrade plain-http webhook deliveries"
		if isLoopback(baseURL) {
			fix = "a localhost URL is unreachable from the Git host — publish Cairn at a public\n" +
				"https:// address (the tls compose profile does this) and set CAIRN_WEBHOOK_BASE_URL to it"
		}
		out = append(out, warnf(name, fix, "webhook base URL %s is not https://", baseURL))
	default:
		out = append(out, okf(name, "base URL %s", baseURL))
	}

	for _, h := range hosts {
		if !secrets[h] {
			out = append(out, warnf(name,
				fmt.Sprintf("set %s and paste the same value into the host's webhook settings", secretEnvFor(h)),
				"%s has no webhook signing secret — its deliveries will be rejected", h))
		}
	}
	return out
}

func isLoopback(url string) bool {
	s := strings.ToLower(url)
	return strings.Contains(s, "localhost") || strings.Contains(s, "127.0.0.1") || strings.Contains(s, "://[::1]")
}

// --- operator auth --------------------------------------------------------

// CheckAuth reports whether the management API and dashboard are protected.
func CheckAuth(adminUsers []string, authDisabled bool, resolvers []string, cookieSecure, publicHTTPS bool) []Result {
	const name = "operator auth"
	var out []Result

	switch {
	case authDisabled:
		out = append(out, warnf(name,
			"unset CAIRN_AUTH_DISABLED and set CAIRN_ADMIN_USERS=<your-host-username> before exposing this server",
			"DISABLED by CAIRN_AUTH_DISABLED — anyone who can reach this server can operate it"))
	case len(adminUsers) == 0:
		out = append(out, warnf(name,
			"set CAIRN_ADMIN_USERS=<your-host-username> (comma-separated for TAs)",
			"open mode — CAIRN_ADMIN_USERS is unset, so the API and dashboard are unprotected"))
	case len(resolvers) == 0:
		out = append(out, failf(name,
			"configure OAuth for the login host, e.g. CAIRN_GITHUB_CLIENT_ID + CAIRN_GITHUB_CLIENT_SECRET,\n"+
				"and set CAIRN_OAUTH_REDIRECT_URL to <public-url>/auth/callback",
			"%d admin user(s) allowlisted, but no OAuth resolver is configured — nobody can log in", len(adminUsers)))
	default:
		out = append(out, okf(name, "%d admin user(s), login via %s", len(adminUsers), strings.Join(resolvers, ", ")))
	}

	if publicHTTPS && !cookieSecure {
		out = append(out, warnf(name,
			"set CAIRN_COOKIE_SECURE=1 so the session cookie is only sent over HTTPS",
			"served over HTTPS but the session cookie lacks the Secure flag"))
	}
	return out
}

// --- dashboard ------------------------------------------------------------

// CheckWebDir reports whether the built dashboard is present. exists should
// report whether the directory contains an index.html.
func CheckWebDir(dir string, exists func(string) error) Result {
	const name = "dashboard"
	if dir == "" {
		return warnf(name,
			"set CAIRN_WEB_DIR to the built assets (web/dist), or use the container image, which bundles them",
			"not mounted — only the plain status page is served at /")
	}
	if err := exists(dir); err != nil {
		return failf(name,
			"build it with `npm --prefix web ci && npm --prefix web run build`, then point CAIRN_WEB_DIR at web/dist",
			"CAIRN_WEB_DIR=%s is not a usable dashboard build: %v", dir, err)
	}
	return okf(name, "serving from %s", dir)
}
