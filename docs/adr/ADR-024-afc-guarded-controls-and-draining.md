# ADR-024: AFC Guarded Controls And Draining

- Status: accepted; Stop origin restriction amended by ADR-026
- Related: ADR-020, ADR-023

## Context

Approvals, user-input responses, Stop, and forced shutdown mutate the AFC-owned
App Server process. Telegram callbacks can arrive late or from the wrong topic,
and force-off must not trade cleanup convenience for lost turn ownership.

## Decision

- AFC approval and structured input callbacks persist the current session,
  topic, thread, turn, request, writer generation, and Telegram message id.
  Every callback re-reads current state and fails closed on any mismatch.
- Only server requests from a current Telegram-origin AFC lease become
  actionable. Passive/Desktop-origin waiting state remains display-only.
- Resolving a request or observing its turn terminal expires every matching AFC
  callback. Telegram send failure does not retain writer ownership.
- In spawned compatibility mode, `/stop` in a managed topic interrupts only its
  current AFC lease. In shared-daemon mode, ADR-026 allows `/stop` to re-read
  the topic's exact durable thread and interrupt its authoritative active turn
  regardless of Desktop or Telegram origin. It never guesses a turn id.
- Safe `/afc off` refuses while starting, active, or unknown leases exist and
  lists their stable topic titles.
- `/afc off --force` first changes the writer/session to `draining`, rejects new
  starts, interrupts each known active AFC turn, and waits for guarded terminal
  evidence. Full confirmation closes the shared writer, then commits logical
  off and starts best-effort topic cleanup.
- A force timeout leaves the session `draining`, keeps topics routable only to
  terminal event/poll processing, performs no cleanup, and does not kill the
  writer blindly. A later force-off may retry.
- Neither safe nor force off restores the global observer, creates a poll/live
  process, or starts/stops the legacy writer.

## Consequences

- Old-session and cross-topic buttons cannot answer current App Server
  requests.
- Topic deletion never precedes terminal confirmation for managed turns.
- Unknown ownership requires explicit operator resolution rather than prompt
  replay or process killing.
