# Contributing to Cairn

Thanks for helping build a tool educators can trust. This is an early-stage
project; the architecture is settled but most behavior is unimplemented.

## Getting started

```sh
go build ./...        # whole tree must compile
go vet ./...
gofmt -l .            # must print nothing
go run ./cmd/cairn     # GET /healthz on :8080
```

## Where to start

- **GitHub adapter** — fill in `pkg/adapter/github`. The interface in
  `pkg/adapter/adapter.go` is the contract; the compile-time assertion keeps you
  honest. Methods must be concurrency-safe and idempotent where the verb implies it.
- **MVP endpoints** — the route stubs in `internal/api/server.go`.

## Ground rules

- **The privacy invariant is non-negotiable.** No code path may persist a
  student's legal name, SIS ID, or plaintext email. The identity anchor is the
  Git-host username. PRs that weaken this will be declined. See `DESIGN.md` §5–6.
- **Respect the host seam.** Host-specific logic lives only behind
  `adapter.Adapter`. The rest of the codebase must not import a host SDK directly.
- **Licensing.** `internal/` and `cmd/` are AGPL-3.0-or-later; `pkg/` is
  Apache-2.0. Keep new files in the right place and carry the matching SPDX header.

## Sign-off (DCO)

Sign commits off (`git commit -s`) to certify the
[Developer Certificate of Origin](https://developercertificate.org/). No CLA.

## Style

Standard `gofmt`/`go vet`. Keep packages small and dependency-light, especially
`pkg/` (it is meant to be importable on its own).
