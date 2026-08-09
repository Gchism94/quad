// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"os"
)

const usage = `cairn — a host-agnostic, privacy-first classroom control plane

Usage:
  cairn [serve]              run the control-plane HTTP server (the default)
  cairn doctor [flags]       check this deployment and say how to fix what is wrong
                             (see docs/deploy.md for the full deployment path)
  cairn import ghc [flags]   import a course from GitHub Classroom
  cairn roster pull [flags]  build a roster from your LMS, matched locally
  cairn help                 show this message

Run "cairn doctor --help", "cairn import ghc --help", or
"cairn roster pull --help" for their flags.
`

func main() {
	args := os.Args[1:]
	// Bare "cairn" has always started the server; it still does.
	if len(args) == 0 {
		serve()
		return
	}

	switch args[0] {
	case "serve":
		serve()

	case "import":
		if len(args) < 2 {
			fatalUsage("cairn import: expected a source, e.g. `cairn import ghc`")
		}
		switch args[1] {
		case "ghc", "github-classroom":
			if err := runImportGHC(args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "cairn import ghc: %v\n", err)
				os.Exit(1)
			}
		default:
			fatalUsage(fmt.Sprintf("cairn import: unknown source %q (supported: ghc)", args[1]))
		}

	case "roster":
		if len(args) < 2 {
			fatalUsage("cairn roster: expected a subcommand, e.g. `cairn roster pull`")
		}
		switch args[1] {
		case "pull":
			if err := runRosterPull(args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "cairn roster pull: %v\n", err)
				os.Exit(1)
			}
		default:
			fatalUsage(fmt.Sprintf("cairn roster: unknown subcommand %q (supported: pull)", args[1]))
		}

	case "doctor":
		if err := runDoctor(args[1:]); err != nil {
			// The report is already on stdout; a failing doctor just exits 1.
			if !errors.Is(err, errUnhealthy) {
				fmt.Fprintf(os.Stderr, "cairn doctor: %v\n", err)
			}
			os.Exit(1)
		}

	case "help", "-h", "--help":
		fmt.Print(usage)

	default:
		fatalUsage(fmt.Sprintf("cairn: unknown command %q", args[0]))
	}
}

func fatalUsage(msg string) {
	fmt.Fprintf(os.Stderr, "%s\n\n%s", msg, usage)
	os.Exit(2)
}
