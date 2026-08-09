// SPDX-License-Identifier: AGPL-3.0-or-later

package grading

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/EduCloud-Ecosystem/cairn/pkg/gradingspec"
)

// oneTest is the minimal spec used throughout this file.
func oneTest() gradingspec.Spec {
	return gradingspec.Spec{Tests: []gradingspec.Test{{Name: "t", Run: "true", Points: 1}}}
}

// The exact argument list the runner produced before isolation tiers existed,
// transcribed from buildRunArgs. This is the regression guard the whole change
// rests on: an existing deployment that has not opted in must invoke the runtime
// byte-for-byte identically to before.
//
// If a future change to buildRunArgs makes this fail, that is the test doing its
// job — do not re-baseline it without deciding that the default path really
// should change.
func defaultArgsBefore(name string) []string {
	return []string{
		"run", "--rm", "--name", name,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--pids-limit", "256",
		"--memory", "512m",
		"--memory-swap", "512m",
		"--cpus", "1",
		"--tmpfs", "/tmp:rw,size=64m",
		"--env", "HOME=/tmp",
		"-v", "/host/checkout:/work",
		"--workdir", "/work",
		"--user", "65534:65534",
		"--network", "none",
		"grader:1", "sh", "-c", "true",
	}
}

func TestDefaultTierArgsAreByteIdenticalToBefore(t *testing.T) {
	for _, tier := range []IsolationTier{"", IsolationShared} {
		t.Run("tier="+string(tier), func(t *testing.T) {
			fr := &fakeRunner{}
			r := &ContainerRunner{
				Runtime: "docker", DefaultImage: "grader:1",
				User: "65534:65534", Isolation: tier, exec: fr,
			}
			_, _ = runContainer(t, r, oneTest())

			if len(fr.calls) != 1 {
				t.Fatalf("expected 1 invocation, got %d", len(fr.calls))
			}
			got := fr.calls[0].args
			// The container name is random; substitute it into the expectation
			// rather than skipping the comparison of every other argument.
			want := defaultArgsBefore(flagValue(got, "--name"))

			if !reflect.DeepEqual(got, want) {
				t.Errorf("default tier arguments changed.\n got: %q\nwant: %q", got, want)
			}
			// Stated separately because it is the property that matters: the
			// default must not select any OCI runtime explicitly.
			if hasArg(got, "--runtime") {
				t.Error("default tier passed --runtime; it must not select a runtime at all")
			}
		})
	}
}

func TestGVisorTierAddsRuntimeFlagAndNothingElse(t *testing.T) {
	fr := &fakeRunner{}
	r := &ContainerRunner{
		Runtime: "docker", DefaultImage: "grader:1",
		User: "65534:65534", Isolation: IsolationGVisor, exec: fr,
	}
	_, _ = runContainer(t, r, oneTest())

	got := fr.calls[0].args
	if v := flagValue(got, "--runtime"); v != "runsc" {
		t.Errorf("--runtime = %q, want %q", v, "runsc")
	}

	// Removing the two --runtime arguments must yield exactly the default list:
	// the tier adds a runtime selection and changes nothing else, so hardening
	// cannot be weakened by opting in.
	var stripped []string
	for i := 0; i < len(got); i++ {
		if got[i] == "--runtime" {
			i++ // skip its value too
			continue
		}
		stripped = append(stripped, got[i])
	}
	want := defaultArgsBefore(flagValue(got, "--name"))
	if !reflect.DeepEqual(stripped, want) {
		t.Errorf("gVisor tier changed more than the runtime flag.\n got: %q\nwant: %q", stripped, want)
	}
}

// The hard requirement: an unrecognised tier is refused. Grading must not run at
// all rather than run with a weaker boundary than was asked for.
func TestUnknownTierRefusesRatherThanDowngrading(t *testing.T) {
	fr := &fakeRunner{}
	r := &ContainerRunner{
		Runtime: "docker", DefaultImage: "grader:1",
		User: "65534:65534", Isolation: IsolationTier("gvsior"), exec: fr, // typo
	}

	_, err := r.Run(context.Background(), oneTest(), "/host/checkout")
	if err == nil {
		t.Fatal("an unknown isolation tier must fail the run, not fall back to the default")
	}
	// The message has to name the bad value and the valid ones, or an operator
	// cannot act on it.
	for _, want := range []string{"gvsior", "shared", "gvisor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message missing %q: %v", want, err)
		}
	}
	// Nothing may have been executed: no container ran under a weaker boundary.
	if len(fr.calls) != 0 {
		t.Errorf("runtime was invoked %d time(s) despite an invalid tier; "+
			"student code must not run when the requested boundary is unavailable", len(fr.calls))
	}
}

func TestValidateIsolation(t *testing.T) {
	tests := []struct {
		tier    IsolationTier
		wantErr bool
	}{
		{"", false},
		{IsolationShared, false},
		{IsolationGVisor, false},
		{"runsc", true},   // the binary's name, not the tier's
		{"none", true},    // plausible-looking, still not valid
		{"SHARED", true},  // case-sensitive on purpose: no near-miss guessing
		{"gvisor ", true}, // trailing space
	}
	for _, tc := range tests {
		r := &ContainerRunner{Isolation: tc.tier}
		err := r.ValidateIsolation()
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateIsolation(%q) error = %v, wantErr %v", tc.tier, err, tc.wantErr)
		}
	}
}

func TestEffectiveIsolationResolvesTheDefault(t *testing.T) {
	if got := (&ContainerRunner{}).EffectiveIsolation(); got != IsolationShared {
		t.Errorf("unset isolation = %q, want %q", got, IsolationShared)
	}
	if got := (&ContainerRunner{Isolation: IsolationGVisor}).EffectiveIsolation(); got != IsolationGVisor {
		t.Errorf("gvisor isolation = %q, want %q", got, IsolationGVisor)
	}
}

// The tier must apply to setup steps as well as tests — a setup step runs the
// same untrusted image and would otherwise get a weaker boundary than the tests.
func TestGVisorTierAppliesToSetupSteps(t *testing.T) {
	fr := &fakeRunner{}
	r := &ContainerRunner{
		Runtime: "docker", DefaultImage: "grader:1",
		User: "65534:65534", Isolation: IsolationGVisor, exec: fr,
	}
	spec := oneTest()
	spec.Setup = []string{"echo setup"}
	_, _ = runContainer(t, r, spec)

	if len(fr.calls) != 2 {
		t.Fatalf("expected setup + test invocations, got %d", len(fr.calls))
	}
	for i, call := range fr.calls {
		if v := flagValue(call.args, "--runtime"); v != "runsc" {
			t.Errorf("invocation %d: --runtime = %q, want runsc", i, v)
		}
	}
}
