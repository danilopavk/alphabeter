# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

- `make up` / `make down`: start/stop Postgres and Redis (docker compose)
- `make test`: start them, then `go test -race ./...`
- `make run`: run the app on http://localhost:8090. It takes the Resend key from
  `ALPHABETER_RESEND_API_KEY` (the shell's `RESEND_API_KEY` belongs to another project); without
  it, emails and their links are printed to the terminal.
- `make backup-test`: end-to-end test of the backup job (needs Docker)
- One test: `go test ./internal/game -run TestRetirement`. Tests that need Postgres/Redis skip
  unless `TEST_DATABASE_URL` and `TEST_REDIS_URL` are set (the Makefile sets both).

Go is pinned to 1.24 (`go.mod`). The newest versions of several dependencies require Go 1.25+ and
would bump the `go` directive, so check a version's `go.mod` before `go get`.

## How we work

TDD: write the failing test first, make it pass, then refactor. Tests use real Postgres and Redis
(`internal/testutil` gives each test a fresh database and a Redis key prefix), not mocks.

## Architecture

One Go binary. Postgres holds all durable state; Redis holds only auth data (access tokens,
auth codes, login rate limits). Templates and static files are embedded.

- `internal/game`: the learning rules, pure (no I/O, randomness passed in). All scores are
  integers in tenths. Also the binary formats: a kid's state is a packed, versioned blob, and
  answers are appended to a compact log that `Replay` turns back into the state. Older state
  versions must stay readable.
- `internal/kids`: stores that state blob plus one answer-log row per kid per UTC day. Answers
  are applied with an optimistic `seq` check, so a double tap counts once.
- `internal/idp`: the app's own identity provider: email/password accounts and an OAuth 2.1
  server (authorization code + PKCE, refresh-token rotation with reuse detection).
- `internal/web`: the parent-facing app. For sign-in it is an ordinary OAuth client of `idp`
  and keeps the tokens in cookies. Pages use htmx fragments.
- `internal/ui`: templates and static assets; asset URLs carry a content hash (`{{asset}}`).
- `internal/clientip`: the visitor's IP behind Railway's proxy and Cloudflare.
- `backup/`: the daily backup, a separate Railway cron service: `pg_dump` uploaded to Cloudflare R2.
- `internal/app`: wires it all into one handler. Configuration (environment variables) is
  documented at the top of `cmd/alphabeter/main.go`.

Production runs on Railway behind Cloudflare, with Resend for email.
