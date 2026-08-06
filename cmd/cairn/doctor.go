// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/EduCloud-Ecosystem/cairn/internal/doctor"
	"github.com/EduCloud-Ecosystem/cairn/pkg/adapter"
	forgejoadapter "github.com/EduCloud-Ecosystem/cairn/pkg/adapter/forgejo"
)

const doctorHelp = `cairn doctor — check this deployment and say how to fix what is wrong

It reads configuration, opens the store, tries to bind the listen address, and —
when grading is configured — starts one short-lived container to verify that
grading can see its work directory. It contacts a Git host only with
--verify-hosts.

Exit status is 0 when there are no failures, 1 otherwise, so it works in a
deployment script:

    docker compose exec cairn cairn doctor

Flags:
`

// knownEnvVars is every CAIRN_* variable Cairn actually reads. It powers the
// check that catches a variable which looks right and is silently ignored — the
// class of bug that had .env.example documenting CAIRN_LISTEN while the code read
// CAIRN_LISTEN_ADDR. Keep this list in sync when adding a variable.
var knownEnvVars = []string{
	"CAIRN_STORE", "CAIRN_SQLITE_PATH", "CAIRN_DATABASE_URL", "CAIRN_DB_AUTOMIGRATE",
	"CAIRN_GITHUB_APP_ID", "CAIRN_GITHUB_INSTALLATION_ID", "CAIRN_GITHUB_PRIVATE_KEY_FILE",
	"CAIRN_GITHUB_BASE_URL", "CAIRN_GITHUB_CLIENT_ID", "CAIRN_GITHUB_CLIENT_SECRET",
	"CAIRN_GITHUB_WEBHOOK_SECRET",
	"CAIRN_FORGEJO_BASE_URL", "CAIRN_FORGEJO_TOKEN", "CAIRN_FORGEJO_GIT_USERNAME",
	"CAIRN_FORGEJO_OAUTH_CLIENT_ID", "CAIRN_FORGEJO_OAUTH_CLIENT_SECRET",
	"CAIRN_FORGEJO_WEBHOOK_SECRET",
	"CAIRN_GITLAB_BASE_URL", "CAIRN_GITLAB_TOKEN", "CAIRN_GITLAB_GIT_USERNAME",
	"CAIRN_GITLAB_OAUTH_CLIENT_ID", "CAIRN_GITLAB_OAUTH_CLIENT_SECRET",
	"CAIRN_GITLAB_WEBHOOK_SECRET",
	"CAIRN_OAUTH_REDIRECT_URL", "CAIRN_OPERATOR_HOST",
	"CAIRN_ADMIN_USERS", "CAIRN_AUTH_DISABLED", "CAIRN_COOKIE_SECURE",
	"CAIRN_GRADER", "CAIRN_GRADER_RUNTIME", "CAIRN_GRADER_IMAGE",
	"CAIRN_GRADER_RESTRICTED_NETWORK", "CAIRN_GRADER_USER",
	"CAIRN_GIT_CLONE_TOKEN",
	"CAIRN_LISTEN_ADDR", "CAIRN_WEB_DIR", "CAIRN_WEBHOOK_BASE_URL",
	"CAIRN_GHC_TOKEN",
}

// deploymentEnvVars are consumed by deploy/docker-compose.yml rather than by the
// binary. They arrive in the container through env_file, so doctor must not
// report them as unrecognized — they are doing exactly what they should.
var deploymentEnvVars = []string{
	"CAIRN_DOMAIN",    // Caddy's certificate hostname (tls profile)
	"CAIRN_WORK_DIR",  // host path bind-mounted for grading checkouts
	"CAIRN_HTTP_BIND", // interface the published port binds to
	"CAIRN_HTTP_PORT", // host port published for the control plane
}

// ignoredEnvVars are names Cairn recognizes but does not act on, mapped to the
// reason. Silently ignoring these is what makes them worth reporting.
var ignoredEnvVars = map[string]string{
	"CAIRN_GRADER_NETWORK": "egress policy is per-spec (limits.network in the grading spec), not global.\n" +
		"use CAIRN_GRADER_RESTRICTED_NETWORK to name the network for \"restricted\" specs",
	"CAIRN_WEBHOOK_URL":         "deprecated alias — rename it to CAIRN_WEBHOOK_BASE_URL",
	"CAIRN_ENABLE_LOCAL_GRADER": "deprecated — use CAIRN_GRADER=local-exec-unsafe instead",
}

func runDoctor(argv []string) error {
	var verifyHosts bool
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), doctorHelp)
		fs.PrintDefaults()
	}
	fs.BoolVar(&verifyHosts, "verify-hosts", false,
		"additionally make one read-only API call per configured Git host to prove the credentials work")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	rep := buildDoctorReport(ctx, verifyHosts)
	rep.Write(os.Stdout)
	if !rep.OK() {
		return errUnhealthy
	}
	return nil
}

// errUnhealthy makes `cairn doctor` exit non-zero without printing a second
// error line on top of the report.
var errUnhealthy = errors.New("doctor: failures reported above")

func buildDoctorReport(ctx context.Context, verifyHosts bool) doctor.Report {
	var rep doctor.Report

	// Store: open it the same way the server does, then perform a trivial read.
	st, storeKind, err := openStore(ctx)
	if err != nil {
		rep.Add(doctor.CheckStore(ctx, fmt.Sprintf("%v", err), nil))
	} else {
		rep.Add(doctor.CheckStore(ctx, storeKind, func(ctx context.Context) error {
			_, err := st.ListClassrooms(ctx)
			return err
		}))
	}

	recognized := append(append([]string{}, knownEnvVars...), deploymentEnvVars...)
	rep.AddAll(doctor.CheckEnvNames(os.Environ(), recognized, ignoredEnvVars)...)

	listenAddr := getenvDefault("CAIRN_LISTEN_ADDR", ":8080")
	rep.Add(doctor.CheckListenAddr(listenAddr, func(addr string) error {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		return l.Close()
	}, cairnAlreadyServing))

	states := adapterStates(ctx, verifyHosts)
	rep.AddAll(doctor.CheckAdapters(states)...)

	var configuredHosts []string
	for _, s := range states {
		if s.Configured {
			configuredHosts = append(configuredHosts, s.Host)
		}
	}
	secrets := map[string]bool{}
	for h, v := range webhookSecretsFromEnv() {
		secrets[string(h)] = v != ""
	}
	baseURL := webhookBaseURLFromEnv()
	rep.AddAll(doctor.CheckWebhooks(baseURL, configuredHosts, secrets, webhookSecretEnvName)...)

	rep.AddAll(doctor.CheckAuth(
		splitCSV(os.Getenv("CAIRN_ADMIN_USERS")),
		os.Getenv("CAIRN_AUTH_DISABLED") == "1",
		configuredResolvers(),
		os.Getenv("CAIRN_COOKIE_SECURE") == "1",
		strings.HasPrefix(baseURL, "https://"),
	)...)

	rep.Add(doctor.CheckWebDir(os.Getenv("CAIRN_WEB_DIR"), func(dir string) error {
		info, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("not a directory")
		}
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			return fmt.Errorf("no index.html inside")
		}
		return nil
	}))

	rep.AddAll(doctor.CheckGrading(ctx, doctor.GradingConfig{
		Mode:    os.Getenv("CAIRN_GRADER"),
		Runtime: getenvDefault("CAIRN_GRADER_RUNTIME", "docker"),
		Image:   os.Getenv("CAIRN_GRADER_IMAGE"),
		WorkDir: os.Getenv("TMPDIR"),
	}, execCommander{}, osProbeFS{})...)

	return rep
}

// adapterStates reports each Git host's configuration, reusing the same env
// readers the server uses so doctor cannot drift from what actually gets wired.
func adapterStates(ctx context.Context, verify bool) []doctor.AdapterState {
	var out []doctor.AdapterState

	gh := doctor.AdapterState{Host: string(adapter.HostGitHub)}
	if a, err := githubAdapterFromEnv(); err != nil {
		gh.Configured, gh.Err = true, err
	} else if a != nil {
		gh.Configured = true
		gh.Detail = fmt.Sprintf("GitHub App %s, installation %s",
			os.Getenv("CAIRN_GITHUB_APP_ID"), os.Getenv("CAIRN_GITHUB_INSTALLATION_ID"))
		if verify {
			gh.Probed = true
			gh.ProbeErr = probeAdapter(ctx, a)
		}
	}
	out = append(out, gh)

	fj := doctor.AdapterState{Host: string(adapter.HostForgejo)}
	if cfg, err := forgejoConfigFromEnv(); err != nil {
		fj.Configured, fj.Err = true, err
	} else if cfg != nil {
		fj.Configured = true
		fj.Detail = cfg.BaseURL
		if a, err := forgejoadapter.NewWithHost(*cfg, adapter.HostForgejo); err != nil {
			fj.Err = err
		} else if verify {
			fj.Probed = true
			fj.ProbeErr = probeAdapter(ctx, a)
		}
	}
	out = append(out, fj)

	gl := doctor.AdapterState{Host: string(adapter.HostGitLab)}
	if a, err := gitlabAdapterFromEnv(); err != nil {
		gl.Configured, gl.Err = true, err
	} else if a != nil {
		gl.Configured = true
		gl.Detail = getenvDefault("CAIRN_GITLAB_BASE_URL", "https://gitlab.com")
		if verify {
			gl.Probed = true
			gl.ProbeErr = probeAdapter(ctx, a)
		}
	}
	out = append(out, gl)

	return out
}

// cairnAlreadyServing reports whether a healthy Cairn answers on addr. The usual
// way to run doctor is inside the running deployment, where the port is held by
// Cairn itself — that is success, not a conflict.
func cairnAlreadyServing(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	return err == nil && strings.Contains(string(body), `"ok"`)
}

// probeAdapter makes one read-only call to prove the credentials are accepted.
// Adapters that do not implement adapter.Prober are reported as unverified
// rather than failed.
func probeAdapter(ctx context.Context, a adapter.Adapter) error {
	p, ok := a.(adapter.Prober)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return p.Probe(ctx)
}

// configuredResolvers lists the hosts that can complete an OAuth login, matching
// the wiring in serve().
func configuredResolvers() []string {
	var out []string
	if os.Getenv("CAIRN_GITHUB_CLIENT_ID") != "" {
		out = append(out, string(adapter.HostGitHub))
	}
	if os.Getenv("CAIRN_FORGEJO_OAUTH_CLIENT_ID") != "" {
		out = append(out, string(adapter.HostForgejo))
	}
	if os.Getenv("CAIRN_GITLAB_OAUTH_CLIENT_ID") != "" {
		out = append(out, string(adapter.HostGitLab))
	}
	return out
}

// webhookSecretEnvName names the variable that sets a host's signing secret.
func webhookSecretEnvName(host string) string {
	return "CAIRN_" + envHostKey(adapter.Host(host)) + "_WEBHOOK_SECRET"
}

// execCommander runs real commands for the container-runtime checks.
type execCommander struct{}

func (execCommander) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// osProbeFS is the real filesystem for the grading work-directory probe.
type osProbeFS struct{}

func (osProbeFS) MkdirTemp(dir, pattern string) (string, error) { return os.MkdirTemp(dir, pattern) }
func (osProbeFS) WriteFile(path string, data []byte) error      { return os.WriteFile(path, data, 0o644) }
func (osProbeFS) RemoveAll(path string) error                   { return os.RemoveAll(path) }
