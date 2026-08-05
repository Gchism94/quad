// SPDX-License-Identifier: AGPL-3.0-or-later

// Command cairn runs the Cairn control-plane HTTP server.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/api"
	"github.com/EduCloud-Ecosystem/cairn/internal/config"
	"github.com/EduCloud-Ecosystem/cairn/internal/grading"
	"github.com/EduCloud-Ecosystem/cairn/internal/identity"
	"github.com/EduCloud-Ecosystem/cairn/internal/provisioning"
	"github.com/EduCloud-Ecosystem/cairn/internal/store"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
	forgejoadapter "github.com/EduCloud-Ecosystem/cairn/pkg/adapter/forgejo"
	githubadapter "github.com/EduCloud-Ecosystem/cairn/pkg/adapter/github"
	gitlabadapter "github.com/EduCloud-Ecosystem/cairn/pkg/adapter/gitlab"
)

// logStartupSummary emits a single structured startup block covering all wired
// components. The goal is to make the serving-shell configuration auditable at a
// glance instead of hunting through scattered log lines.
func logStartupSummary(storeKind string, adapters map[adapter.Host]adapter.Adapter,
	resolvers map[adapter.Host]identity.Resolver, loginHost adapter.Host,
	grader bool, webDir, webhookBaseURL string, webhookSecrets map[adapter.Host]string) {

	// Adapters
	adapterHosts := make([]string, 0, len(adapters))
	for h := range adapters {
		adapterHosts = append(adapterHosts, string(h))
	}
	adapterLine := strings.Join(adapterHosts, ", ")
	if adapterLine == "" {
		adapterLine = "NONE — set CAIRN_GITHUB_* and/or CAIRN_FORGEJO_*"
	}

	// Resolvers
	resolverHosts := make([]string, 0, len(resolvers))
	for h := range resolvers {
		resolverHosts = append(resolverHosts, string(h))
	}
	resolverLine := strings.Join(resolverHosts, ", ")
	if resolverLine == "" {
		resolverLine = "NONE"
	}

	log.Printf("store: %s", storeKind)
	log.Printf("adapters registered: %s", adapterLine)
	log.Printf("identity resolvers: %s  operator-host: %s", resolverLine, loginHost)
	if grader {
		log.Printf("grading: configured")
	} else {
		log.Printf("grading: DISABLED — grade requests will be rejected (set CAIRN_GRADER=container)")
	}
	if webDir != "" {
		log.Printf("dashboard: serving from %s", webDir)
	} else {
		log.Printf("dashboard: not mounted — set CAIRN_WEB_DIR=web/dist (status page at /)")
	}
	if webhookBaseURL != "" {
		log.Printf("webhook base URL: %s (per repo: %s/webhooks/<host>)", webhookBaseURL, strings.TrimRight(webhookBaseURL, "/"))
	} else {
		log.Printf("webhook base URL: not set — push webhooks will not be registered")
	}
	// Webhook signing secrets per host (set/unset). A host without a secret cannot
	// have its deliveries verified, so the receiver will reject them.
	for _, h := range []adapter.Host{adapter.HostGitHub, adapter.HostForgejo, adapter.HostGitea, adapter.HostGitLab} {
		state := "unset — deliveries will be rejected (set CAIRN_" + envHostKey(h) + "_WEBHOOK_SECRET)"
		if webhookSecrets[h] != "" {
			state = "set"
		}
		log.Printf("webhook secret [%s]: %s", h, state)
	}
}

// envHostKey maps a host to the env-var infix used for its webhook secret.
// Gitea shares Forgejo's CAIRN_FORGEJO_WEBHOOK_SECRET.
func envHostKey(h adapter.Host) string {
	switch h {
	case adapter.HostGitHub:
		return "GITHUB"
	case adapter.HostGitLab:
		return "GITLAB"
	default:
		return "FORGEJO"
	}
}

func main() {
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, storeKind, err := openStore(ctx)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	queue := provisioning.NewService(st)

	resolvers := map[adapter.Host]identity.Resolver{}
	if os.Getenv("CAIRN_GITHUB_CLIENT_ID") != "" {
		resolvers[adapter.HostGitHub] = identity.NewGitHub(
			os.Getenv("CAIRN_GITHUB_CLIENT_ID"),
			os.Getenv("CAIRN_GITHUB_CLIENT_SECRET"),
			os.Getenv("CAIRN_OAUTH_REDIRECT_URL"),
		)
	}
	if os.Getenv("CAIRN_FORGEJO_OAUTH_CLIENT_ID") != "" {
		// The Gitea OAuth2 flow is identical to Forgejo's, so register the same
		// resolver under both host values — a student claiming a host: gitea
		// classroom is routed by the resolvers-map key.
		for _, host := range []adapter.Host{adapter.HostForgejo, adapter.HostGitea} {
			resolvers[host] = identity.NewForgejoWithHost(
				os.Getenv("CAIRN_FORGEJO_OAUTH_CLIENT_ID"),
				os.Getenv("CAIRN_FORGEJO_OAUTH_CLIENT_SECRET"),
				os.Getenv("CAIRN_OAUTH_REDIRECT_URL"),
				os.Getenv("CAIRN_FORGEJO_BASE_URL"),
				host,
			)
		}
	}
	if os.Getenv("CAIRN_GITLAB_OAUTH_CLIENT_ID") != "" {
		resolvers[adapter.HostGitLab] = identity.NewGitLab(
			os.Getenv("CAIRN_GITLAB_OAUTH_CLIENT_ID"),
			os.Getenv("CAIRN_GITLAB_OAUTH_CLIENT_SECRET"),
			os.Getenv("CAIRN_OAUTH_REDIRECT_URL"),
			os.Getenv("CAIRN_GITLAB_BASE_URL"),
		)
	}
	// Operator-login host: explicit env, else GitHub, then Forgejo, then GitLab.
	loginHost := adapter.Host(os.Getenv("CAIRN_OPERATOR_HOST"))
	if _, ok := resolvers[loginHost]; !ok {
		switch {
		case resolvers[adapter.HostGitHub] != nil:
			loginHost = adapter.HostGitHub
		case resolvers[adapter.HostForgejo] != nil:
			loginHost = adapter.HostForgejo
		case resolvers[adapter.HostGitLab] != nil:
			loginHost = adapter.HostGitLab
		}
	}

	adapters := map[adapter.Host]adapter.Adapter{}
	if gh, err := githubAdapterFromEnv(); err != nil {
		log.Printf("github adapter not configured: %v (repo provisioning will fail until set)", err)
	} else if gh != nil {
		adapters[adapter.HostGitHub] = gh
	}
	// Forgejo and Gitea are one adapter family: a hard fork sharing the same
	// /api/v1 surface, configured by the same CAIRN_FORGEJO_* vars. Register the
	// configured instance under BOTH host values (each stamped with its own host)
	// so a classroom may declare host: forgejo or host: gitea against the same
	// server.
	if fjCfg, err := forgejoConfigFromEnv(); err != nil {
		log.Printf("forgejo/gitea adapter not configured: %v (Forgejo/Gitea provisioning will fail until set)", err)
	} else if fjCfg != nil {
		for _, host := range []adapter.Host{adapter.HostForgejo, adapter.HostGitea} {
			ad, err := forgejoadapter.NewWithHost(*fjCfg, host)
			if err != nil {
				log.Printf("forgejo/gitea adapter (%s): %v", host, err)
				continue
			}
			adapters[host] = ad
		}
	}
	if gl, err := gitlabAdapterFromEnv(); err != nil {
		log.Printf("gitlab adapter not configured: %v (GitLab provisioning will fail until set)", err)
	} else if gl != nil {
		adapters[adapter.HostGitLab] = gl
	}

	// Per-host webhook secrets sign push deliveries; the receiver verifies them.
	// The Forgejo secret covers both forgejo and gitea (one Gitea-family instance).
	webhookSecrets := webhookSecretsFromEnv()
	webhookBaseURL := webhookBaseURLFromEnv()

	worker := &provisioning.Worker{
		Store:          st,
		Adapters:       adapters,
		WebhookBaseURL: webhookBaseURL,
		WebhookSecrets: webhookSecrets,
		Poll:           2 * time.Second,
	}

	// Grading executes untrusted code. The runner is chosen explicitly via
	// CAIRN_GRADER; nothing runs student code unless configured. See graderFromEnv.
	grader := graderFromEnv(st)
	if grader != nil {
		worker.Grader = grader
	}

	go worker.Run(ctx)

	scheduler := &provisioning.Scheduler{Store: st, Queue: queue, Interval: time.Minute}
	go scheduler.Run(ctx)

	webDir := os.Getenv("CAIRN_WEB_DIR")

	admins := splitCSV(os.Getenv("CAIRN_ADMIN_USERS"))
	authEnabled := os.Getenv("CAIRN_AUTH_DISABLED") != "1" && len(admins) > 0

	logStartupSummary(storeKind, adapters, resolvers, loginHost, grader != nil, webDir, webhookBaseURL, webhookSecrets)

	if !authEnabled {
		log.Printf("WARNING: operator authentication is DISABLED — the management API and dashboard are unprotected; set CAIRN_ADMIN_USERS (and operator OAuth) to enable it")
	} else if len(resolvers) == 0 || loginHost == "" {
		log.Printf("WARNING: operator auth is enabled but no OAuth resolver is configured — operator login will not work until CAIRN_GITHUB_CLIENT_ID or CAIRN_FORGEJO_OAUTH_CLIENT_ID is set")
	}

	srv := api.New(api.Options{
		Store:            st,
		Queue:            queue,
		Resolvers:        resolvers,
		Adapters:         adapters,
		WebhookSecrets:   webhookSecrets,
		LoginHost:        loginHost,
		WebDir:           webDir,
		AuthEnabled:      authEnabled,
		AdminUsers:       admins,
		CookieSecure:     os.Getenv("CAIRN_COOKIE_SECURE") == "1",
		GraderConfigured: grader != nil,
	})
	log.Printf("cairn control plane listening on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
		log.Fatal(err)
	}
}

// splitCSV parses a comma-separated env value into a trimmed, non-empty list.
func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// graderFromEnv selects the grading runner from the environment. It returns nil
// (no grader; grade jobs fail with a clear message) unless CAIRN_GRADER is set.
//
//	CAIRN_GRADER=container         sandboxed container runner (recommended)
//	CAIRN_GRADER=local-exec-unsafe host exec runner — NO isolation; trusted use only
//
// Container options: CAIRN_GRADER_RUNTIME (docker|podman), CAIRN_GRADER_IMAGE
// (default image), CAIRN_GRADER_NETWORK (none|restricted),
// CAIRN_GRADER_RESTRICTED_NETWORK (runtime network name), CAIRN_GRADER_USER.
// schemeHostFromURL extracts the scheme (e.g. "http") and host (e.g.
// "localhost:3000") from a base URL. Returns ("", "") when raw is empty or
// unparseable. The clone path uses both so a plain-http instance is not forced
// through TLS.
func schemeHostFromURL(raw string) (scheme, host string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", ""
	}
	return u.Scheme, u.Host
}

func graderFromEnv(st store.Store) provisioning.Grader {
	// Build a per-host credential map so the checkout knows which scheme, hostname,
	// and token to use for each adapter.Host. The token is never embedded in the
	// clone URL — it is delivered via GIT_ASKPASS (H1 credential hygiene).
	ghScheme, ghHost := "https", "github.com"
	if b := os.Getenv("CAIRN_GITHUB_BASE_URL"); b != "" {
		if s, h := schemeHostFromURL(b); h != "" {
			ghScheme, ghHost = s, h // GHES: use the enterprise scheme + hostname
		}
	}
	hosts := map[adapter.Host]grading.CloneCreds{
		adapter.HostGitHub: {
			Scheme:   ghScheme,
			Hostname: ghHost,
			Username: "x-access-token",
			Token:    os.Getenv("CAIRN_GIT_CLONE_TOKEN"),
		},
	}
	if base := os.Getenv("CAIRN_FORGEJO_BASE_URL"); base != "" {
		if s, h := schemeHostFromURL(base); h != "" {
			creds := grading.CloneCreds{
				Scheme:   s,
				Hostname: h,
				Username: getenvDefault("CAIRN_FORGEJO_GIT_USERNAME", "oauth2"),
				Token:    os.Getenv("CAIRN_FORGEJO_TOKEN"),
			}
			// Same instance, both host labels — a host: gitea classroom's repos
			// clone with the same credentials.
			hosts[adapter.HostForgejo] = creds
			hosts[adapter.HostGitea] = creds
		}
	}
	if tok := os.Getenv("CAIRN_GITLAB_TOKEN"); tok != "" {
		// GitLab HTTPS clone uses oauth2:<token> — oauth2 as the username (the
		// opposite of Forgejo). Base URL defaults to https://gitlab.com.
		if s, h := schemeHostFromURL(getenvDefault("CAIRN_GITLAB_BASE_URL", "https://gitlab.com")); h != "" {
			hosts[adapter.HostGitLab] = grading.CloneCreds{
				Scheme:   s,
				Hostname: h,
				Username: getenvDefault("CAIRN_GITLAB_GIT_USERNAME", "oauth2"),
				Token:    tok,
			}
		}
	}
	checkout := grading.NewGitCheckout(hosts)

	switch os.Getenv("CAIRN_GRADER") {
	case "container":
		runtime := getenvDefault("CAIRN_GRADER_RUNTIME", "docker")
		cr := &grading.ContainerRunner{
			Runtime:           runtime,
			DefaultImage:      os.Getenv("CAIRN_GRADER_IMAGE"),
			RestrictedNetwork: os.Getenv("CAIRN_GRADER_RESTRICTED_NETWORK"),
			User:              os.Getenv("CAIRN_GRADER_USER"),
		}
		log.Printf("grading: container runner (runtime=%s image=%q)", runtime, cr.DefaultImage)
		if cr.DefaultImage == "" {
			log.Printf("note: CAIRN_GRADER_IMAGE is unset; grading requires each spec to set its own image")
		}
		return grading.NewService(st, cr, checkout)

	case "local-exec-unsafe":
		log.Printf("WARNING: local-exec grader runs untrusted code with NO sandbox; use only for trusted/local material")
		return grading.NewService(st, grading.NewExecRunner(), checkout)

	default:
		// Back-compat: the original opt-in flag maps to the unsafe exec runner.
		if os.Getenv("CAIRN_ENABLE_LOCAL_GRADER") == "1" {
			log.Printf("DEPRECATED: CAIRN_ENABLE_LOCAL_GRADER is deprecated; use CAIRN_GRADER=local-exec-unsafe instead")
			log.Printf("WARNING: local-exec grader runs untrusted code with NO sandbox; use only for trusted/local material")
			return grading.NewService(st, grading.NewExecRunner(), checkout)
		}
		return nil
	}
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// webhookBaseURLFromEnv resolves the public base URL Cairn registers webhooks
// against. The worker appends /webhooks/<host> per repo. CAIRN_WEBHOOK_BASE_URL is
// the current name; CAIRN_WEBHOOK_URL is accepted as a deprecated alias (it used to
// be a full URL, but is now treated as the base for forward compatibility).
func webhookBaseURLFromEnv() string {
	if base := os.Getenv("CAIRN_WEBHOOK_BASE_URL"); base != "" {
		return base
	}
	if legacy := os.Getenv("CAIRN_WEBHOOK_URL"); legacy != "" {
		log.Printf("note: CAIRN_WEBHOOK_URL is deprecated — rename to CAIRN_WEBHOOK_BASE_URL; it is now treated as a base URL and Cairn appends /webhooks/<host> per repo")
		return legacy
	}
	return ""
}

// webhookSecretsFromEnv builds the per-host push-webhook signing secrets. The
// Forgejo secret is registered under both forgejo and gitea, since a single
// Gitea-family instance serves both host labels. Hosts with no secret are omitted.
func webhookSecretsFromEnv() map[adapter.Host]string {
	secrets := map[adapter.Host]string{}
	if s := os.Getenv("CAIRN_GITHUB_WEBHOOK_SECRET"); s != "" {
		secrets[adapter.HostGitHub] = s
	}
	if s := os.Getenv("CAIRN_FORGEJO_WEBHOOK_SECRET"); s != "" {
		secrets[adapter.HostForgejo] = s
		secrets[adapter.HostGitea] = s
	}
	if s := os.Getenv("CAIRN_GITLAB_WEBHOOK_SECRET"); s != "" {
		secrets[adapter.HostGitLab] = s
	}
	return secrets
}

// forgejoConfigFromEnv reads the shared CAIRN_FORGEJO_* configuration for the
// Gitea-family adapter. It returns (nil, nil) when neither variable is set. The
// same config backs both the Forgejo and Gitea host registrations.
func forgejoConfigFromEnv() (*forgejoadapter.Config, error) {
	base, tok := os.Getenv("CAIRN_FORGEJO_BASE_URL"), os.Getenv("CAIRN_FORGEJO_TOKEN")
	if base == "" && tok == "" {
		return nil, nil // not configured
	}
	if base == "" || tok == "" {
		return nil, fmt.Errorf("forgejo: set both CAIRN_FORGEJO_BASE_URL and CAIRN_FORGEJO_TOKEN")
	}
	return &forgejoadapter.Config{BaseURL: base, Token: tok}, nil
}

// gitlabAdapterFromEnv builds a GitLab adapter when CAIRN_GITLAB_TOKEN is set.
// BaseURL defaults to https://gitlab.com. Returns (nil, nil) when unconfigured.
func gitlabAdapterFromEnv() (*gitlabadapter.Adapter, error) {
	tok := os.Getenv("CAIRN_GITLAB_TOKEN")
	if tok == "" {
		return nil, nil // not configured
	}
	return gitlabadapter.New(gitlabadapter.Config{
		BaseURL: os.Getenv("CAIRN_GITLAB_BASE_URL"), // empty → gitlab.com
		Token:   tok,
	})
}

// githubAdapterFromEnv builds a GitHub App adapter when the relevant environment
// variables are present. It returns (nil, nil) when unconfigured.
func githubAdapterFromEnv() (*githubadapter.Adapter, error) {
	appIDStr := os.Getenv("CAIRN_GITHUB_APP_ID")
	if appIDStr == "" {
		return nil, nil
	}
	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil {
		return nil, err
	}
	instID, err := strconv.ParseInt(os.Getenv("CAIRN_GITHUB_INSTALLATION_ID"), 10, 64)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(os.Getenv("CAIRN_GITHUB_PRIVATE_KEY_FILE"))
	if err != nil {
		return nil, err
	}
	return githubadapter.New(githubadapter.Config{
		AppID:          appID,
		InstallationID: instID,
		PrivateKeyPEM:  keyPEM,
	})
}
