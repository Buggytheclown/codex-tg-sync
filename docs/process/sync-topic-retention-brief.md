# Sync Topic Retention Brief

## Problem

Long-running Sync sessions accumulate more Telegram topics than are useful to
the operator. Failed cleanup from an older Sync session can also remain stranded
because startup currently retries only the most recent session.

## Decision

- Discover only Codex chats created or updated in the last 24 hours.
- On every Sync reconciliation, prune topics whose Codex thread `updatedAt` is
  more than 24 hours old, even when no new topic is created.
- Keep Sync active when there are no fresh chats at activation.
- Prune empty ready drafts whose stored `updated_at` is more than 24 hours old.
- Never prune starting, active, or unknown work.
- Mark selected rows `cleanup` transactionally before Telegram deletion.
- Return from new-topic creation after that durable mark; only after the ready response is delivered does a service-owned background loop perform Telegram deletion outside the `/newchat` critical path.
- Delete the SQLite row only after Telegram confirms deletion. A failed deletion
  remains durable for a later retry.
- Treat Telegram's `TOPIC_ID_INVALID` delete response as confirmation that the
  topic is already absent, so stale cleanup rows do not persist forever.
- Retry cleanup rows from every Sync session during startup, activation,
  periodic reconciliation, and new-topic pruning.
- Cleanup failure never rolls back successful creation of the new topic.

Codex App Server threads remain authoritative and are never deleted by this
policy. Telegram topics can be materialized again by a later Sync activation.

## Verification

- Storage selects all inactive rows older than 24 hours by Codex thread activity,
  even if their Telegram topic was recently edited.
- Active/starting/unknown topics and drafts are preserved even when old.
- All chats active within 24 hours remain visible, even when there are more
  than ten.
- New project-backed drafts trigger pruning after persistence.
- A blocked Telegram deletion does not delay the ready response for a new project-backed draft.
- Startup retries cleanup rows belonging to older sessions.
