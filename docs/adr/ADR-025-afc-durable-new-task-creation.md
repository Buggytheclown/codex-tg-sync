# ADR-025: AFC Durable New Task Creation

- Status: accepted
- Related: ADR-023, ADR-024

## Context

AFC must create new Codex tasks from a phone without retaining a first prompt
inside callback state. App Server `thread/start` does not create the rollout
required by a later `thread/resume`; the first `turn/start` must therefore run
on the same writer process as `thread/start`. Thread creation cannot be rolled
back safely when a later Telegram operation fails.

## Decision

- `/projects` and `/newchat` in AFC Control render the same AFC-scoped project
  picker. While AFC is off or draining they fail closed without callback or
  App Server state.
- Project callbacks persist only current session, Control topic, project, and
  CWD metadata. They are consumed before `thread/start`, so duplicate callback
  delivery never creates a second thread.
- Creation first stores one Telegram draft topic with selected project and cwd
  metadata. The callback never calls `thread/start` and stores no prompt.
- The first prompt is a new Telegram message inside the ready draft. Claiming
  that message reserves the shared AFC writer, performs `thread/start`, claims
  the returned durable thread id, materializes the normal AFC binding, and
  performs `turn/start` on the same writer process.
- Timeout/EOF during `thread/start` is ownership unknown and the claimed source
  message is never replayed. A definitive rejection returns the draft to ready
  state for a later message.
- A definitive first `turn/start` failure returns the Telegram topic to draft
  state and retains the empty Codex thread. An ambiguous result remains
  ownership-unknown and is never replayed.
- Pre-migration empty bindings recover only from the precise `no rollout found`
  resume failure and only when no turn was ever rendered.
- The first prompt supplies a short initial name for the Telegram topic and,
  best-effort, the Codex thread.

## Consequences

- Draft topics accept exactly one first message before their durable Codex
  binding exists.
- AFC does not hold the shared writer while waiting for that first message.
- Telegram partial failure never deletes a successfully created Codex thread.
- New tasks can join the current AFC session beyond its original activation
  snapshot only through an explicit user action.
