# Cron Launch Requests Brief

## Problem

The operator needs one local scheduled Codex request per calendar period without
running a second daemon or duplicating the existing external launch lifecycle.

## Goal

- Read enabled daily or single-weekday schedules from `~/.codex-tg/cron.json`.
- After the first matching slot of the local day or week, enqueue one durable
  `ExternalLaunchRequest` for that task and scheduled occurrence date.
- Require Telegram launch approval by default, with an explicit per-task
  auto-start option.
- Preserve task `cwd`, model, and reasoning effort through first-turn dispatch.

## Non-goals

- Replaying every missed calendar occurrence.
- More than one request per task and local calendar period.
- Monthly, multiple-weekday, or multiple-run-per-period scheduling.
- A second queue, worker, daemon, or external write API.
- Editing schedules from Telegram.

## UX / Operator Flow

The operator creates `cron.json`. A due task with `launch_policy` set to
`telegram` appears in the existing external-request Telegram topic with
`Dismiss` and `Start`. `auto` skips that launch gate. Changes affect future
requests only; an already durable request is not rewritten.

## Domain Model

- Cron source identity: `cron`.
- External identity: `<task-id>:<scheduled YYYY-MM-DD>` in the task timezone.
- Execution settings: `model` and `reasoning_effort` on the durable external
  request.
- `(source, external_id)` remains the idempotency authority for every request
  state, including pending, dismissed, failed, and completed.

## Architecture

`internal/cronpoller` parses and validates the JSON file, evaluates the current
local period, and calls a narrow enqueue sink. The existing external launch store,
Telegram approval renderer, conditional claim, Sync topic materialization, and App
Server dispatch remain authoritative.

## Testing

- Daily and weekly due-after-time, missed-period, restart, and before-time tests.
- Invalid JSON/schedule and launch-policy validation tests.
- Storage round-trip for model and reasoning effort.
- External dispatch test proving the exact model and effort reach `turn/start`.
- Full `go test ./...` and `go build -buildvcs=false ./...`.

## Acceptance Criteria

- [x] Starting after the daily cron time creates one request for today.
- [x] Starting after five missed days creates no backlog, only today's request.
- [x] Re-polling or restarting on the same day creates no duplicate request.
- [x] Telegram approval is the default and can be changed to auto-start in JSON.
- [x] The configured cwd, model, reasoning effort, and prompt reach Codex.
- [x] A weekly task catches up once after its weekday slot and never replays missed weeks.
- [x] Catch-up waits for continuous runtime after a sleep-sized polling gap.
- [x] Optional `max_lateness` can bound same-period catch-up.
