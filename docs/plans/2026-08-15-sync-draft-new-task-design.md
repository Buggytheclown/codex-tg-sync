# Sync Draft New Task Design

## Problem

An App Server `thread/start` response is not yet a resumable durable thread.
The rollout needed by `thread/resume` appears only when the first turn starts.
The original Sync new-task flow closed its writer after `thread/start`, created a
Telegram topic, and tried to resume the empty thread after the first topic
message. App Server correctly rejected that resume with `no rollout found`.

Control also accepted `/projects` and `/newchat` without listing them in its
fallback help, and a newly created topic kept an opaque thread-id title after
the first prompt.

## Decision

- A project callback creates a durable Telegram draft topic, not an App Server
  thread. Draft metadata stores the Sync session, Telegram topic, selected cwd,
  project identity, presentation title, and dispatch state.
- The first plain-text message claims the draft exactly once. Sync then reserves
  the shared writer without a thread id, calls `thread/start`, claims the
  returned thread id, materializes the normal Sync topic binding, and calls
  `turn/start` on the same App Server process.
- A definitive failure before `thread/start` or during `thread/start` rejects
  only that source message and makes the draft ready for another message. An
  ambiguous App Server result leaves ownership unknown and is never replayed.
- A definitive first `turn/start` failure converts the topic back to a draft;
  the empty Codex thread is retained because its creation cannot be rolled back
  safely. An ambiguous result remains a normal ownership-unknown Sync binding.
- Existing pre-migration empty Sync bindings are recognized only after the
  precise `no rollout found` resume failure and only when they have no rendered
  turn. Sync converts that binding to a claimed draft and continues the same
  source message through the new first-turn path.
- The first prompt produces a short Telegram-safe title. Sync starts and
  durably records the turn first, asks App Server to use the same thread name
  best-effort, and schedules Telegram rename through normal post-response
  delivery reconciliation.
- `/sync off` treats ready drafts as cleanup targets. A starting or
  ownership-unknown draft blocks safe off just like unfinished bound work.
- Sync Control fallback help lists `/projects` and `/newchat` alongside the
  lifecycle commands it already accepts.

## Non-goals

- Do not keep an App Server writer open while waiting for a phone message.
- Do not create a synthetic bootstrap turn.
- Do not change Sync status/final message content or presentation lifecycle.
- Do not attach drafts to legacy bindings, panels, or the global observer.
- Do not add a second App Server state authority.

## Tests

- Project callback creates one Telegram draft and performs no App Server call.
- First draft prompt performs `thread/start` followed by `turn/start` on one
  writer, creates the durable binding, records the source receipt, and leaves
  Telegram rename to post-response reconciliation.
- Duplicate callbacks and duplicate source messages cannot create another
  thread.
- `no rollout found` on a legacy empty binding recovers through a replacement
  first-turn dispatch; unrelated resume errors remain terminal rejections.
- Draft failure, unknown ownership, safe-off, cleanup, and Control help have
  focused regression coverage.

## Live acceptance

1. Use `/newchat` in Control and select a project.
2. Confirm the ready topic appears without a new Codex thread/rollout error.
3. Send one prompt in that topic.
4. Confirm the topic receives a human-readable name, a new status message
   appears, progress edits that message, and the final arrives normally.
5. Repeat once in a pre-migration empty topic to confirm compatibility repair.
