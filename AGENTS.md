# AGENTS

Purpose: help AI agents work on `codex-tg` without increasing complexity or weakening the operator-facing Telegram control loop.

Good changes are small, evidence-backed, easy to understand, and validated through the same AFC forum surface the operator uses.

## Repository Purpose

`codex-tg` is a Go daemon and local Codex Control Plane. Its supported Telegram product is one private forum group: Codex chats are synchronized into topics, while Control hosts commands, launch requests, health notifications, and poller status.

The repository is public. Never commit private paths, tokens, Telegram ids, local sessions, databases, logs, screenshots with private data, or environment-specific credentials.

## Working Mode

- Read nearby code, tests, the relevant ADR, and `docs/testing/regression-map.md` before editing.
- Prefer inspect -> plan -> test -> implement -> refactor -> validate.
- Do not add frameworks, abstractions, dependencies, or cross-platform machinery without an immediate need.
- For non-trivial behavior changes, write or update the test first.
- Meaningful changes require a fresh-context review of the brief, diff, tests, ADRs, and validation output.
- Telegram-facing behavior requires live QA when a live contour is available.
- After compaction or handoff, reread this file before continuing.

## Context Pull Map

- Telegram routing, topics, approvals, input, lifecycle, or rendering: read `docs/testing/regression-map.md` and ADR-032.
- Public commands or product behavior: read `docs/research/contract-matrix.md`.
- Feature planning or review handoff: read `docs/process/agent-workflow.md` and the related brief.
- Releases or public positioning: read README plus the active wiki pages.
- Historical ADRs and release notes document earlier behavior. ADR-032 supersedes their direct-message observer contracts.

## Core Decisions

- Codex App Server is authoritative for interactive threads, turns, approvals, live events, history, and snapshots.
- `threadId` is durable identity. Telegram topic ids are adapter routing state.
- Telegram ingress is accepted only from exactly one configured user in the configured AFC forum group.
- Reject out-of-scope messages and callbacks before SQLite lookup or App Server work.
- Telegram commands are removed from the default Bot API scope and registered only for the AFC group.
- AFC synchronization uses one polling App Server session and generation-aware writer ownership for mutations.
- Shared daemon and loopback WebSocket transports fail closed; they never silently spawn a private server.
- Startup is non-blocking. Full thread synchronization must not run synchronously in startup.
- SQLite stores AFC topics and drafts, callback routes, launch requests, delivery metadata, snapshots, and daemon state.
- Fresh databases must not create retired DM binding, observer, panel, or approval tables. Existing tables remain inert and are not dropped.
- Do not add a compatibility router for retired direct-message behavior.

## Design Principles

- Keep modules focused and interfaces explicit.
- Prefer guard clauses and one source of truth for routing and lifecycle decisions.
- Inject IO, time, config, and external dependencies where tests need control.
- Make ownership and generation checks explicit around threads, turns, callbacks, and writers.
- Preserve App Server wire-event compatibility even when a former Telegram presentation is removed.
- Use current terms consistently: AFC group, Control, topic, thread, turn, receipt, launch request, poller, delivery.
- Remove dead code introduced or exposed by the change.

## Telegram Product Contract

- The exact private AFC forum group is the only supported Telegram surface.
- Control is the General topic and is a permanent prerequisite.
- `/sync on` materializes recent Codex chats and continuously discovers new ones; `/sync off` cleans managed topics without interrupting Codex work.
- Topic text starts or steers the authoritative turn; `/stop` interrupts it.
- Current messages are `[User]`, `[Status]`, `[Approval]`, `[Input]`, and `[Final]`.
- Approval and input callbacks are scoped to topic, thread, turn, request, writer generation, and message route.
- Resolving an action edits the same card to terminal text and removes buttons.
- `Allow command prefix` maps to App Server `acceptForSession`; it does not approve every command in a session.
- Direct Telegram starts explicitly use `on-request`, `auto_review`, and `workspace-write`.
- External launch requests may carry their own explicit execution settings.
- `/projects` and `/newchat` create topic drafts from known local workspaces; Telegram never accepts arbitrary filesystem paths.
- `/pollers` reports source poller activity and health. `/requests` lists active launch requests.
- Health and external terminal deliveries stay inside the AFC group.
- Sync resets to off after a daemon restart; the operator enables it again after reconnect.

## Routing And Ownership

1. Verify exact group and exact user.
2. Resolve Control, existing AFC topic, or topic draft.
3. For callbacks, load a current callback route and verify all ownership coordinates.
4. Claim the writer generation before mutation.
5. Re-read App Server authority before stale-active recovery or replacement turn start.

Never infer a thread from a visual label or emoji. Persisted AFC topic state and callback tokens are routing authority.

## Testing Discipline

- Test public behavior through stable interfaces; mock only external or non-deterministic boundaries.
- Keep tests deterministic and isolated.
- Boundary tests must prove rejected Telegram updates cannot reach storage or App Server.
- Preserve coverage for AFC activation/reconciliation, prompt/steer/stop, writer ownership, approvals/input, long finals, health, launch requests, poller status, delivery retry, and startup cleanup.
- Schema tests must cover both fresh databases and non-destructive opening of an old database.
- If a check cannot run, report the exact blocker and required manual check.

## Definition Of Done

For Telegram-facing changes:

1. Run the smallest relevant tests.
2. Run `go test ./...`.
3. Run targeted race tests when concurrency or lifecycle changed.
4. Run `go build -buildvcs=false ./...`.
5. Run `git diff --check` and the secret/local scan below.
6. Rebuild and restart the daemon when the user requested deployment.
7. If a live contour exists, verify command scope, Control commands, topic behavior, edit-in-place callbacks, and logs/readback.

Bot API success alone is not live E2E evidence. If live QA is unavailable, say exactly why.

## Repository Layout

- `cmd/ctr-go/`: CLI, setup, service management.
- `internal/appserver/`: App Server transport, snapshots, writer ownership.
- `internal/config/`: config file and environment parsing.
- `internal/control/`, `internal/controlapi/`: reusable local control surfaces.
- `internal/daemon/`: AFC orchestration, launch requests, pollers, health, delivery.
- `internal/storage/`: SQLite schema and repositories.
- `internal/telegram/`: Telegram Bot API transport.
- `internal/tgformat/`: Telegram entity rendering.
- `tests/`: black-box and live-gated tests.
- `docs/`: ADRs, briefs, current wiki, validation notes, historical releases.

## Commit And Release Discipline

- Keep diffs focused.
- Before committing, inspect dirty state, staged diff, Git identity, tests, build, and secret scan.
- Before pushing, verify branch, upstream, ahead/behind, and intended commits.
- Handoffs state branch, commit hashes, push status, checks, docs/ADR changes, and restart/live-QA status.

Required checks:

```powershell
go test ./...
go build -buildvcs=false ./...
git diff --check
rg -n "BOT_TOKEN|TELEGRAM_BOT_TOKEN|api_hash|api_id|phone|password|secret|\\.session|\\.sqlite|\\.env|C:\\\\Users\\\\<private-user>" .
```
