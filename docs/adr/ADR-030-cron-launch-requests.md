# ADR-030: Cron Launch Requests

- Status: accepted
- Amends: ADR-028
- Related: ADR-025, ADR-026, ADR-029

## Context

The operator needs a small number of local recurring Codex tasks to enter the
same durable Telegram approval and Sync dispatch lifecycle as YMessenger launch
requests. A second daemon, queue, or App Server writer would duplicate existing
ownership and recovery rules.

## Decision

- `internal/cronpoller` is an in-process source adapter. It reads
  `~/.codex-tg/cron.json` every 30 seconds and enqueues generic external launch
  requests through the existing service boundary.
- Missing configuration disables the source. Invalid configuration fails
  closed for that poll and creates no requests.
- The accepted five-field subset supports daily schedules and weekly schedules
  selecting exactly one weekday. Monthly rules, weekday lists/ranges, and
  multiple periods per task remain invalid.
- A task becomes due after its first matching slot in the current local day or
  Monday-based calendar week. Missed periods are not replayed. Startup after
  the current period's slot creates one catch-up request; startup before it
  waits.
- The in-process runner requires four minutes of continuous polling after
  startup or a polling gap longer than 90 seconds before evaluating due work.
  This keeps short laptop DarkWake windows from creating launch requests while
  the host network is only partially available.
- A task may set `max_lateness` to a positive Go duration such as `2h`. When
  present, a scheduled occurrence older than that bound is skipped. Omitting
  the field preserves period-wide catch-up.
- Cron external identity is `<task-id>:<scheduled local YYYY-MM-DD>`. The
  existing unique `(source, external_id)` constraint enforces at most one
  durable request per task and period across every request state and restart.
- `launch_policy=telegram` is the default and uses the existing permanent
  external-request topic. `launch_policy=auto` uses the existing durable
  auto-start claim. Policy changes affect future requests only.
- External launch requests persist optional `model` and `reasoning_effort`.
  Dispatch sends explicit values through App Server default collaboration mode
  without changing global Telegram model settings.

## Consequences

The scheduler has no cursor, run table, new queue, or new worker. SQLite launch
request identity is the only period deduplication authority, while App Server
remains authoritative for threads and turns.

The one-day-or-one-week restriction keeps catch-up deterministic. Resume
stabilization can delay an on-time request by four minutes. Monthly,
multiple-weekday, multiple-run-per-period, and replay-all semantics require a
separate decision rather than silently changing this contract.

## Non-goals

- Editing schedules from Telegram.
- Replaying one request for every missed slot.
- Mutating already-created requests after JSON changes.
- A generic shell-command scheduler.
- A second App Server runtime or external write API.
