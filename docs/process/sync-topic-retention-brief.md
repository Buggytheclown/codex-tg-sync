# Sync Topic Retention Brief

## Problem

Long-running Sync sessions accumulate more Telegram topics than are useful to
the operator. Failed cleanup from an older Sync session can also remain stranded
because startup currently retries only the most recent session.

## Decision

- Keep at most ten current managed topics when enough inactive work exists.
- After a new topic is durably recorded, prune the oldest topics whose stored
  `updated_at` is more than 24 hours old.
- Never prune starting, active, or unknown work. Ready empty drafts are eligible.
- Mark selected rows `cleanup` transactionally before Telegram deletion.
- Return from new-topic creation after that durable mark; only after the ready response is delivered does a service-owned background loop perform Telegram deletion outside the `/newchat` critical path.
- Delete the SQLite row only after Telegram confirms deletion. A failed deletion
  remains durable for a later retry.
- Treat Telegram's `TOPIC_ID_INVALID` delete response as confirmation that the
  topic is already absent, so stale cleanup rows do not persist forever.
- Retry cleanup rows from every Sync session during startup, activation, and
  new-topic pruning.
- Cleanup failure never rolls back successful creation of the new topic.

Codex App Server threads remain authoritative and are never deleted by this
policy. Telegram topics can be materialized again by a later Sync activation.

## Verification

- Storage selects only the oldest eligible rows and only the amount needed to
  reach ten logical current topics.
- Active/starting/unknown topics and drafts are preserved even when old.
- If too few inactive topics exist, Sync is allowed to remain above ten.
- New project-backed drafts trigger pruning after persistence.
- A blocked Telegram deletion does not delay the ready response for a new project-backed draft.
- Startup retries cleanup rows belonging to older sessions.
