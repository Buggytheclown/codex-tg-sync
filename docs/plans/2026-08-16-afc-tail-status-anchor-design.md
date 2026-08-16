# AFC Tail Status Anchor

## Product Contract

An AFC topic keeps one live `[Status]` message for the current turn. The live
status is the last bridge message in the topic and is edited in place as
progress changes, matching the useful legacy presentation behavior. It must not
remain anchored above a later Telegram prompt or AFC dispatch acknowledgement.

Completed-turn history remains unchanged. A new turn still receives its own
status message and final notification.

Within an active turn, App Server returns `LatestAgentMessages` newest-first.
When no plan or tool progress is available, AFC renders element zero as the
current commentary. Each later Codex reasoning/commentary block therefore edits
the same live status message; older blocks are fallback history, not the active
status detail.

## Design

Telegram cannot move an edited message. After a successful AFC topic dispatch,
the bridge first delivers its existing direct acknowledgement. The delivery
registration hook then reconciles the mapped AFC topic:

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

The operation runs under the AFC presentation lock and is scoped by exact
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
- Legacy lifecycle tests remain unchanged.
- Live Telegram readback verifies that a steer leaves `[Status]` last and later
  progress edits that same message.
- Multi-commentary readback verifies that each newer Codex block replaces the
  status detail in place while preserving the status message id.
