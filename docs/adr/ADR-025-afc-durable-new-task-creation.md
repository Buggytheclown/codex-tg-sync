# ADR-025: AFC Durable New Task Creation

- Status: accepted
- Related: ADR-023, ADR-024

## Context

AFC must create new Codex tasks from a phone without retaining a first prompt
inside callback state or exposing a half-created Telegram topic as routable.
Thread creation cannot be rolled back safely when a later Telegram operation
fails.

## Decision

- `/projects` and `/newchat` in AFC Control render the same AFC-scoped project
  picker. While AFC is off or draining they fail closed without callback or
  App Server state.
- Project callbacks persist only current session, Control topic, project, and
  CWD metadata. They are consumed before `thread/start`, so duplicate callback
  delivery never creates a second thread.
- Creation reserves the shared AFC writer without a thread claim, performs
  `thread/start`, claims the returned durable thread id, and stores the thread.
  It then creates one Telegram topic and stores the AFC topic binding.
- The binding is routable only after `thread → topic → afc_topics` succeeds.
  The creation callback never calls `turn/start` and stores no prompt.
- The first prompt is a new Telegram message inside the ready managed topic and
  follows normal receipt and writer ownership rules.
- If topic creation fails after thread creation, the Codex thread remains and
  no AFC binding is written. There is no rollback or automatic topic retry.
- Timeout/EOF during `thread/start` is ownership unknown and the consumed
  callback is never replayed. A definitive rejection aborts the reservation.

## Consequences

- New tasks cannot accept input before their durable topic binding exists.
- Telegram partial failure never deletes a successfully created Codex thread.
- New tasks can join the current AFC session beyond its original activation
  snapshot only through an explicit user action.
