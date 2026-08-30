# Sync Tail Status Anchor

## Product Contract

An Sync topic keeps one live `[Status]` message for the current turn. The live
status is the last bridge message in the topic and is edited in place as
progress changes, matching the useful legacy presentation behavior. It must not
remain anchored above a later Telegram prompt, Sync dispatch acknowledgement, or
Telegram service message produced by an Sync topic rename.

Completed-turn history remains unchanged. A new turn still receives its own
status message and final notification.

Within an active turn, App Server returns `LatestAgentMessages` newest-first.
When no plan or tool progress is available, Sync renders element zero as the
current commentary. Each later Codex reasoning/commentary block therefore edits
the same live status message; older blocks are fallback history, not the active
status detail.

## Design

Telegram cannot move an edited message. After a successful Sync topic dispatch,
the bridge first delivers its existing direct acknowledgement. The delivery
registration hook then reconciles the mapped Sync topic:

1. If the stored status belongs to the same active turn, clear its delivery
   anchor and best-effort delete that old live-status message.
2. Read the authoritative shared-daemon snapshot and send a fresh silent status
   at the bottom of the topic.
3. Persist the new message id. Later events and polls edit this new tail status
   normally.

If the acknowledgement is for a new turn, the previous turn's status is
historical and is not deleted; normal new-turn delivery creates the new tail
status. If deletion or the immediate read fails, delivery remains reset so the
existing reconciliation/hot-poll loop retries without blocking the prompt.

A successful topic rename follows the same active-turn reanchor rule. After the
rename, the bridge clears and best-effort deletes only the status whose stored
turn id matches the current non-terminal turn, then renders its replacement from
the already persisted compact snapshot. Aggregate blocks and their effective
timings therefore remain unchanged. A previous-turn or terminal status remains
historical and is never deleted by rename reconciliation.

The operation runs under the Sync presentation lock and is scoped by exact
`chatId`, `topicId`, `threadId`, and `turnId`. Legacy panels, direct messages,
final fingerprints, and writer ownership are unchanged.

## Rejected Alternatives

- Keeping the old status and sending another leaves stale duplicates.
- Sending a status message for every poll creates Telegram spam.
- Reanchoring before the direct acknowledgement races with the acknowledgement
  and can still leave status above it.

## Verification

- Same-turn steer: acknowledgement is delivered, old live status is deleted,
  and one fresh status is sent after it and becomes the persisted anchor.
- New turn: previous-turn status is retained and a new status is appended.
- Delete/read failure: dispatch remains successful and later reconciliation can
  recreate the status.
- Active-turn rename: the old live status is deleted and one replacement is
  sent after the rename service message with identical aggregate blocks and
  effective block timings.
- Previous-turn and terminal statuses are retained when a topic is renamed.
- Legacy lifecycle tests remain unchanged.
- Live Telegram readback verifies that a steer leaves `[Status]` last and later
  progress edits that same message.
- Multi-commentary readback verifies that each newer Codex block replaces the
  status detail in place while preserving the status message id.
