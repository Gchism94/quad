// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Every non-ok result must tell the operator what to change. This is the whole
// point of the package, so it is asserted structurally rather than case by case.
func requireFixes(t *testing.T, results []Result) {
	t.Helper()
	for _, r := range results {
		if r.Status != StatusOK && strings.TrimSpace(r.Fix) == "" {
			t.Errorf("%s: %q is a %s with no fix", r.Name, r.Detail, r.Status)
		}
	}
}

func statuses(results []Result) []Status {
	out := make([]Status, len(results))
	for i, r := range results {
		out[i] = r.Status
	}
	return out
}

func hasStatus(results []Result, s Status) bool {
	for _, r := range results {
		if r.Status == s {
			return true
		}
	}
	return false
}

func TestCheckStore(t *testing.T) {
	ctx := context.Background()

	t.Run("reachable", func(t *testing.T) {
		got := CheckStore(ctx, "sqlite /var/lib/cairn/cairn.db", func(context.Context) error { return nil })
		if got.Status != StatusOK {
			t.Errorf("status = %v (%s)", got.Status, got.Detail)
		}
	})

	t.Run("memory warns about data loss", func(t *testing.T) {
		got := CheckStore(ctx, "memory (ephemeral)", func(context.Context) error { return nil })
		if got.Status != StatusWarn {
			t.Errorf("status = %v", got.Status)
		}
		requireFixes(t, []Result{got})
	})

	// The two failure modes need completely different fixes, so they must not be
	// collapsed into one generic "database problem".
	t.Run("unmigrated is distinguished from unreachable", func(t *testing.T) {
		unmigrated := CheckStore(ctx, "postgres", func(context.Context) error {
			return errors.New(`ERROR: relation "classrooms" does not exist (SQLSTATE 42P01)`)
		})
		if unmigrated.Status != StatusFail {
			t.Fatalf("status = %v", unmigrated.Status)
		}
		if !strings.Contains(unmigrated.Fix, "CAIRN_DB_AUTOMIGRATE") {
			t.Errorf("unmigrated fix should point at automigrate, got %q", unmigrated.Fix)
		}

		unreachable := CheckStore(ctx, "postgres", func(context.Context) error {
			return errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
		})
		if unreachable.Status != StatusFail {
			t.Fatalf("status = %v", unreachable.Status)
		}
		if strings.Contains(unreachable.Fix, "CAIRN_DB_AUTOMIGRATE") {
			t.Errorf("unreachable fix should not suggest migrating, got %q", unreachable.Fix)
		}
		if !strings.Contains(unreachable.Fix, "compose service name") {
			t.Errorf("unreachable fix should mention the Docker hostname trap, got %q", unreachable.Fix)
		}
	})

	t.Run("unopenable", func(t *testing.T) {
		got := CheckStore(ctx, "boom", nil)
		if got.Status != StatusFail {
			t.Errorf("status = %v", got.Status)
		}
	})
}

func TestCheckEnvNames(t *testing.T) {
	known := []string{"CAIRN_LISTEN_ADDR", "CAIRN_ADMIN_USERS", "CAIRN_GRADER", "CAIRN_WEBHOOK_BASE_URL"}
	ignored := map[string]string{"CAIRN_GRADER_NETWORK": "policy is per-spec"}

	t.Run("clean environment", func(t *testing.T) {
		got := CheckEnvNames([]string{"CAIRN_GRADER=container", "PATH=/bin", "HOME=/root"}, known, ignored)
		if len(got) != 1 || got[0].Status != StatusOK {
			t.Errorf("got %+v", got)
		}
	})

	// The regression this check exists for: .env.example documented CAIRN_LISTEN
	// while the code read CAIRN_LISTEN_ADDR, and nothing said so.
	t.Run("truncated name suggests the real one", func(t *testing.T) {
		got := CheckEnvNames([]string{"CAIRN_LISTEN=:9000"}, known, ignored)
		if len(got) != 1 || got[0].Status != StatusWarn {
			t.Fatalf("got %+v", got)
		}
		if !strings.Contains(got[0].Fix, "CAIRN_LISTEN_ADDR") {
			t.Errorf("fix should suggest CAIRN_LISTEN_ADDR, got %q", got[0].Fix)
		}
	})

	t.Run("typo suggests the real one", func(t *testing.T) {
		got := CheckEnvNames([]string{"CAIRN_ADMIN_USER=alice"}, known, ignored)
		if !strings.Contains(got[0].Fix, "CAIRN_ADMIN_USERS") {
			t.Errorf("fix = %q", got[0].Fix)
		}
	})

	t.Run("recognized but ignored variables say why", func(t *testing.T) {
		got := CheckEnvNames([]string{"CAIRN_GRADER_NETWORK=none"}, known, ignored)
		if len(got) != 1 || got[0].Status != StatusWarn {
			t.Fatalf("got %+v", got)
		}
		if !strings.Contains(got[0].Detail, "no effect") || got[0].Fix != "policy is per-spec" {
			t.Errorf("got %+v", got[0])
		}
	})

	t.Run("unrelated name is not force-matched", func(t *testing.T) {
		got := CheckEnvNames([]string{"CAIRN_TOTALLY_MADE_UP_THING=1"}, known, ignored)
		if strings.Contains(got[0].Fix, "did you mean") {
			t.Errorf("should not guess for an unrelated name, got %q", got[0].Fix)
		}
	})
}

func TestCheckListenAddr(t *testing.T) {
	inUse := func(string) error { return errors.New("address already in use") }
	free := func(string) error { return nil }

	t.Run("free port", func(t *testing.T) {
		if got := CheckListenAddr(":8080", free, func(string) bool { return false }); got.Status != StatusOK {
			t.Errorf("status = %v", got.Status)
		}
	})

	// The normal way to run doctor is `docker compose exec cairn cairn doctor`,
	// which shares a network namespace with the server. If holding your own port
	// counted as a failure, a healthy deployment could never report green.
	t.Run("port held by a healthy Cairn is success", func(t *testing.T) {
		got := CheckListenAddr(":8080", inUse, func(string) bool { return true })
		if got.Status != StatusOK {
			t.Fatalf("status = %v (%s)", got.Status, got.Detail)
		}
		if !strings.Contains(got.Detail, "already served") {
			t.Errorf("detail = %q", got.Detail)
		}
	})

	t.Run("port held by a stranger fails", func(t *testing.T) {
		got := CheckListenAddr(":8080", inUse, func(string) bool { return false })
		if got.Status != StatusFail {
			t.Errorf("status = %v", got.Status)
		}
		requireFixes(t, []Result{got})
	})

	t.Run("nil serving probe still fails safely", func(t *testing.T) {
		if got := CheckListenAddr(":8080", inUse, nil); got.Status != StatusFail {
			t.Errorf("status = %v", got.Status)
		}
	})
}

func TestCheckAdapters(t *testing.T) {
	t.Run("none configured warns", func(t *testing.T) {
		got := CheckAdapters([]AdapterState{{Host: "github"}, {Host: "gitlab"}})
		if len(got) != 1 || got[0].Status != StatusWarn {
			t.Fatalf("got %+v", got)
		}
		requireFixes(t, got)
	})

	t.Run("broken credentials fail", func(t *testing.T) {
		got := CheckAdapters([]AdapterState{
			{Host: "github", Configured: true, Err: errors.New("open key.pem: no such file")},
		})
		if !hasStatus(got, StatusFail) {
			t.Errorf("got %+v", statuses(got))
		}
		requireFixes(t, got)
	})

	t.Run("configured without probe is honest about not verifying", func(t *testing.T) {
		got := CheckAdapters([]AdapterState{{Host: "github", Configured: true, Detail: "App 1"}})
		if got[0].Status != StatusOK {
			t.Fatalf("got %+v", got)
		}
		if !strings.Contains(got[0].Detail, "not verified") {
			t.Errorf("detail should not overclaim: %q", got[0].Detail)
		}
	})

	t.Run("rejected credentials fail", func(t *testing.T) {
		got := CheckAdapters([]AdapterState{
			{Host: "github", Configured: true, Detail: "App 1", Probed: true, ProbeErr: errors.New("401")},
		})
		if got[0].Status != StatusFail {
			t.Errorf("got %+v", got)
		}
		requireFixes(t, got)
	})
}

func TestCheckWebhooks(t *testing.T) {
	envFor := func(h string) string { return "CAIRN_" + strings.ToUpper(h) + "_WEBHOOK_SECRET" }

	t.Run("no hosts means nothing to say", func(t *testing.T) {
		if got := CheckWebhooks("", nil, nil, envFor); got != nil {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		got := CheckWebhooks("https://cairn.example.edu", []string{"github"}, map[string]bool{"github": true}, envFor)
		for _, r := range got {
			if r.Status != StatusOK {
				t.Errorf("unexpected %v: %s", r.Status, r.Detail)
			}
		}
	})

	t.Run("localhost base URL explains it is unreachable", func(t *testing.T) {
		got := CheckWebhooks("http://localhost:8080", []string{"github"}, map[string]bool{"github": true}, envFor)
		if !hasStatus(got, StatusWarn) {
			t.Fatalf("got %+v", statuses(got))
		}
		if !strings.Contains(got[0].Fix, "unreachable from the Git host") {
			t.Errorf("fix = %q", got[0].Fix)
		}
	})

	t.Run("missing secret names the variable to set", func(t *testing.T) {
		got := CheckWebhooks("https://c.example.edu", []string{"github"}, map[string]bool{}, envFor)
		if !hasStatus(got, StatusWarn) {
			t.Fatalf("got %+v", statuses(got))
		}
		var found bool
		for _, r := range got {
			if strings.Contains(r.Fix, "CAIRN_GITHUB_WEBHOOK_SECRET") {
				found = true
			}
		}
		if !found {
			t.Errorf("no result named the secret variable: %+v", got)
		}
	})
}

func TestCheckAuth(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		got := CheckAuth([]string{"alice"}, false, []string{"github"}, true, true)
		if got[0].Status != StatusOK {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("open mode warns", func(t *testing.T) {
		got := CheckAuth(nil, false, []string{"github"}, false, false)
		if got[0].Status != StatusWarn {
			t.Errorf("got %+v", got)
		}
		requireFixes(t, got)
	})

	t.Run("explicitly disabled warns", func(t *testing.T) {
		got := CheckAuth([]string{"alice"}, true, []string{"github"}, false, false)
		if got[0].Status != StatusWarn || !strings.Contains(got[0].Detail, "DISABLED") {
			t.Errorf("got %+v", got)
		}
	})

	// Allowlisted admins with no OAuth resolver locks everyone out, including the
	// operator — that is a failure, not a warning.
	t.Run("admins without a resolver fail", func(t *testing.T) {
		got := CheckAuth([]string{"alice"}, false, nil, false, false)
		if got[0].Status != StatusFail {
			t.Errorf("got %+v", got)
		}
		requireFixes(t, got)
	})

	t.Run("https without secure cookie warns", func(t *testing.T) {
		got := CheckAuth([]string{"alice"}, false, []string{"github"}, false, true)
		if !hasStatus(got, StatusWarn) {
			t.Errorf("got %+v", statuses(got))
		}
	})
}

func TestCheckWebDir(t *testing.T) {
	if got := CheckWebDir("", nil); got.Status != StatusWarn {
		t.Errorf("unset should warn, got %v", got.Status)
	}
	if got := CheckWebDir("/srv/web", func(string) error { return nil }); got.Status != StatusOK {
		t.Errorf("got %v", got.Status)
	}
	got := CheckWebDir("/srv/web", func(string) error { return errors.New("no index.html inside") })
	if got.Status != StatusFail {
		t.Errorf("got %v", got.Status)
	}
	requireFixes(t, []Result{got})
}

func TestReportWriteAndOK(t *testing.T) {
	var r Report
	r.Add(okf("store", "sqlite"))
	r.Add(warnf("grading", "set CAIRN_GRADER=container", "disabled"))
	if !r.OK() {
		t.Error("warnings alone must not make a report unhealthy")
	}

	r.Add(failf("listen address", "free the port\nor pick another", ":8080 in use"))
	if r.OK() {
		t.Error("a failure must make the report unhealthy")
	}

	var sb strings.Builder
	r.Write(&sb)
	out := sb.String()
	for _, want := range []string{"ok", "warn", "fail", "→ set CAIRN_GRADER=container", "→ free the port", "→ or pick another", "FAILURE(S)"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}

	// Zero-valued results are skipped so callers can add conditionally.
	before := len(r.Results)
	r.Add(Result{})
	if len(r.Results) != before {
		t.Error("zero-valued result should be ignored")
	}
}
