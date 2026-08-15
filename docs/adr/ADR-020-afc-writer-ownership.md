# ADR-020: AFC and Legacy Writer Ownership

- Status: accepted
- Amends: ADR-001, ADR-012, ADR-019
- Related: `docs/testing/regression-map.md`

## Context

The daemon needs a full AFC surface in one configured Telegram forum group while
preserving the existing bot-DM product. Both surfaces can mutate Codex threads,
but two independent App Server processes must never resume or start turns in the
same thread concurrently.

An App Server process may keep a resumed thread loaded after a turn becomes
terminal. A terminal event is therefore not sufficient evidence that another
process can safely take ownership. Startup ambiguity is also significant: when
the daemon cannot determine whether a mutating request reached App Server, it
must not replay the prompt or kill the process blindly.

## Decision

- The daemon uses one generic generation-aware lazy writer manager, instantiated
  independently for legacy DM mutations and AFC group mutations.
- A shared process-local thread-claim registry coordinates the two managers.
- A writer reserves a thread before any mutating App Server call.
- A claim belongs to a writer process generation and remains until that process
  closes successfully. Turn terminal clears active work but does not directly
  release the claim.
- Writer work has explicit `starting`, `active`, and `unknown` states. Ambiguous
  dispatch moves to `unknown`; it is neither replayed nor treated as terminal.
- A writer process closes after its last known work becomes terminal or is
  safely aborted. A close error keeps the manager in `closing` and retains all
  claims.
- JSON-RPC writes to one stdio App Server process are serialized across normal
  requests, notifications, and server-request responses.
- Different writer processes may execute different threads concurrently.
- Read-only polling uses a separate long-lived client and `thread/read`; polling
  does not acquire writer claims and must not call `thread/resume`.

## AFC and legacy consequences

- A legacy turn that started before AFC activation may finish normally, and its
  approvals and Stop controls remain on the legacy DM surface.
- New legacy mutations of an AFC-managed thread fail before an App Server
  mutation while another process generation owns that thread.
- AFC shutdown drains and closes only the AFC writer. It does not interrupt a
  legacy writer that owns other threads.
- `/afc off` does not restore the observer, start an eager legacy writer, or
  resume tracked legacy threads. A later explicit legacy mutation may lazily
  start the legacy writer.

## Non-goals

- Distributed ownership across multiple daemon processes.
- Prompt journaling or automatic replay after an ambiguous dispatch.
- Blind writer termination to accelerate handback.
- Treating passive polling as proof of live Desktop ownership.
