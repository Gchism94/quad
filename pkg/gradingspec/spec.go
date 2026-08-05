// SPDX-License-Identifier: Apache-2.0

// Package gradingspec defines the portable, host-neutral grading specification
// that lives in an assignment's template repo (conventionally grading.yaml). It
// describes how to build and test student code and how to turn test outcomes
// into a score, independently of any particular CI system — so the same spec
// runs on Cairn's sandboxed runners, GitHub Actions, or Forgejo Actions.
//
// Permissively licensed (Apache-2.0) so tooling outside Cairn can read and write it.
package gradingspec

import "time"

// Version is the current spec schema version.
const Version = "1"

// Spec is the top-level grading specification.
type Spec struct {
	Version string   `yaml:"version" json:"version"`
	Image   string   `yaml:"image" json:"image"`                     // container image tests run in
	Setup   []string `yaml:"setup,omitempty" json:"setup,omitempty"` // runs once before tests
	Tests   []Test   `yaml:"tests" json:"tests"`
	Limits  Limits   `yaml:"limits" json:"limits"` // defaults applied to every test
}

// Test is a single graded check.
type Test struct {
	Name string `yaml:"name" json:"name"`
	// Run is the command whose exit code (0 = pass) determines the outcome,
	// unless Match is set.
	Run string `yaml:"run" json:"run"`
	// Points awarded when the test passes.
	Points float64 `yaml:"points" json:"points"`
	// Match, if set, scores by comparing stdout to an expectation instead of by
	// exit code.
	Match *OutputMatch `yaml:"match,omitempty" json:"match,omitempty"`
	// Limits overrides the spec-level limits for this test.
	Limits *Limits `yaml:"limits,omitempty" json:"limits,omitempty"`
}

// OutputMatch scores a test by comparing captured stdout to an expectation.
type OutputMatch struct {
	Expected string `yaml:"expected" json:"expected"`
	Trim     bool   `yaml:"trim,omitempty" json:"trim,omitempty"`
}

// NetworkPolicy controls egress for untrusted code. The zero value denies all.
type NetworkPolicy string

const (
	NetworkNone       NetworkPolicy = "none"       // default: no egress
	NetworkRestricted NetworkPolicy = "restricted" // allowlist only
)

// Limits are the sandbox resource constraints for running untrusted student code.
type Limits struct {
	Timeout  time.Duration `yaml:"timeout" json:"timeout"`     // wall-clock limit per step
	MemoryMB int           `yaml:"memory_mb" json:"memory_mb"` // memory cap
	CPUs     float64       `yaml:"cpus" json:"cpus"`           // CPU-core cap
	Network  NetworkPolicy `yaml:"network" json:"network"`     // egress policy; default deny
}

// MaxScore returns the total points available across all tests.
func (s Spec) MaxScore() float64 {
	var total float64
	for _, t := range s.Tests {
		total += t.Points
	}
	return total
}
