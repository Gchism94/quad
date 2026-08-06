// SPDX-License-Identifier: AGPL-3.0-or-later

// Package doctor implements Cairn's self-diagnostics: the checks behind
// `cairn doctor`.
//
// The package is deliberately side-effect-free. Every check that would touch the
// world — open a port, reach a database, run a container — takes that capability
// as a function argument, so the whole suite is testable offline and the checks
// themselves stay pure decision logic.
//
// The rule for every result: a failure or a warning MUST carry a Fix that names
// the exact thing to change. An instructor running this on a fresh VM has no
// other source of truth.
package doctor

import (
	"fmt"
	"io"
	"strings"
)

// Status is the outcome of a single check.
type Status int

const (
	// StatusOK means the check passed and nothing needs doing.
	StatusOK Status = iota
	// StatusWarn means Cairn will run, but something is degraded, insecure, or
	// silently ignored.
	StatusWarn
	// StatusFail means Cairn will not work correctly until this is fixed.
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	default:
		return "fail"
	}
}

// Result is one check's outcome.
type Result struct {
	// Name is the subsystem, e.g. "store" or "grading".
	Name   string
	Status Status
	// Detail is what was observed.
	Detail string
	// Fix is what to do about it. Required whenever Status is not StatusOK.
	Fix string
}

// Report is the full run.
type Report struct {
	Results []Result
}

// Add appends a result, ignoring zero-valued ones so callers can build a report
// with conditional checks.
func (r *Report) Add(res Result) {
	if res.Name == "" {
		return
	}
	r.Results = append(r.Results, res)
}

// AddAll appends several results.
func (r *Report) AddAll(res ...Result) {
	for _, x := range res {
		r.Add(x)
	}
}

// Counts returns how many results landed in each status.
func (r Report) Counts() (ok, warn, fail int) {
	for _, res := range r.Results {
		switch res.Status {
		case StatusOK:
			ok++
		case StatusWarn:
			warn++
		default:
			fail++
		}
	}
	return
}

// OK reports whether the deployment is usable: no failures. Warnings do not make
// a report unhealthy — plenty of valid setups run without grading or webhooks.
func (r Report) OK() bool {
	_, _, fail := r.Counts()
	return fail == 0
}

// Write renders the report. Failures and warnings are followed by an indented
// arrow line carrying the fix, which is the part an operator actually acts on.
func (r Report) Write(w io.Writer) {
	width := 0
	for _, res := range r.Results {
		if len(res.Name) > width {
			width = len(res.Name)
		}
	}

	fmt.Fprintln(w, "cairn doctor")
	fmt.Fprintln(w)
	for _, res := range r.Results {
		fmt.Fprintf(w, "  %-4s  %-*s  %s\n", res.Status, width, res.Name, res.Detail)
		if res.Status != StatusOK && res.Fix != "" {
			for _, line := range strings.Split(res.Fix, "\n") {
				fmt.Fprintf(w, "  %-4s  %-*s  → %s\n", "", width, "", line)
			}
		}
	}

	ok, warn, fail := r.Counts()
	fmt.Fprintln(w)
	switch {
	case fail > 0:
		fmt.Fprintf(w, "%d ok, %d warning(s), %d FAILURE(S) — Cairn will not work correctly until the failures above are fixed.\n", ok, warn, fail)
	case warn > 0:
		fmt.Fprintf(w, "%d ok, %d warning(s), no failures — Cairn is usable; the warnings above are worth reading.\n", ok, warn)
	default:
		fmt.Fprintf(w, "%d ok — everything checks out.\n", ok)
	}
}

// okf builds a passing result.
func okf(name, format string, args ...any) Result {
	return Result{Name: name, Status: StatusOK, Detail: fmt.Sprintf(format, args...)}
}

// warnf builds a warning. fix must say what to change.
func warnf(name, fix, format string, args ...any) Result {
	return Result{Name: name, Status: StatusWarn, Detail: fmt.Sprintf(format, args...), Fix: fix}
}

// failf builds a failure. fix must say what to change.
func failf(name, fix, format string, args ...any) Result {
	return Result{Name: name, Status: StatusFail, Detail: fmt.Sprintf(format, args...), Fix: fix}
}
