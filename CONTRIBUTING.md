# Contributing to BreakerBox

Thanks for your interest! The project is in pre-alpha; the fastest way to help right now is to try it, break it, and file crisp issues.

## Development setup

Requirements: Go 1.26+, Node 20+, pnpm 9+, (optional) Docker for the Linux test suite.

```sh
git clone https://github.com/breakerbox/breakerbox
cd breakerbox
make test     # Go tests across all modules
make dev      # hub on :8090 + Vite dev server on :5173
```

## Repository layout

- `pkg/protocol/` — the hub⇄agent wire contract. Golden-file tests pin the JSON encoding; changing them is a protocol change and needs discussion.
- `hub/` — PocketBase-based server (schema in `hub/migrations/`, agent WS plane in `internal/agenthub/`).
- `agent/` — the host agent (supervision in `internal/supervisor/`, metrics in `internal/collector/`).
- `web/` — the dashboard SPA, embedded into the hub binary at build time.
- `cmd/testapp/` — deterministic test process used by supervisor and e2e tests.

## Ground rules

- **Scope discipline**: BreakerBox is a switchboard, not a PaaS. Deployment/builds/reverse proxies/app stores are non-goals; PRs adding them will be declined kindly.
- **Security invariants** (see SECURITY.md) are non-negotiable: no shell/exec verbs, no shared secrets, no root agents, host-side approval stays.
- Keep the agent dependency-light — it must remain a small static binary.
- Tests accompany behavior changes; `go test ./...` must pass on macOS and Linux (CI runs Windows too).

## Commit / PR conventions

- Small, focused PRs beat big ones.
- Describe the user-visible behavior change in the PR body.
- For anything protocol- or schema-touching, open an issue first.
