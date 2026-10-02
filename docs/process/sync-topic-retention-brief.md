# Sync Topic Retention Brief

## Goal

Telegram is a disposable view of recent Codex work. Every eligible chat active
within the last 24 hours can appear, including more than 100 chats. Old passive
topics may be removed; new Codex activity materializes them again. Codex retains
the complete history.

## Decision

- Activation and discovery paginate the authoritative thread list and exclude
  archived/internal chats and chats with neither creation nor update in the last
  24 hours. There is no total topic-count limit.
- `CTR_GO_SYNC_INITIAL_TOPIC_LIMIT` bounds both initial activation and creation
  attempts per discovery pass (default five). Later passes connect the rest.
  Discovery rotates past failed candidates so a rejected batch cannot starve
  other fresh chats.
- Keep Sync active when no fresh chats exist. Start/status events wake the same
  discovery, and periodic reconciliation covers missed events.
- On every connected Sync reconciliation, mark passive topics whose Codex
  `updatedAt` is older than 24 hours, plus old empty ready drafts, as cleanup.
  Passive Desktop-running/unknown state and undelivered old Finals do not extend
  retention. They can disappear from Telegram and return on fresh activity.
- Existing local Telegram `starting`, `active`, and `unknown` ownership remains
  protected to avoid discarding accepted command/receipt coordination. This is
  local dispatch safety, not general protection of every Desktop-active turn.
- Mark cleanup transactionally. A background worker deletes Telegram topics and
  forgets their rows only after confirmation; `TOPIC_ID_INVALID` means absent.
  Failed deletions remain durable and are retried across historical sessions.
- New-topic cleanup never delays the ready response or rolls back its creation.
- On first catch-up, a completed result whose source `updatedAt` predates Sync
  activation is absorbed without replaying its Final. User/current Status remain
  visible. Results observed after activation are delivered normally. Missing or
  same-second timestamps are handled conservatively by normal delivery.
- Persist a session-local creation marker before Telegram I/O. Unknown create
  outcomes stop automatic creation until the operator inspects untracked topics
  and explicitly cycles Sync off/on. Status and one durable Control warning
  explain the pause. A known persisted binding safely clears an interrupted
  marker. This uses existing `daemon_state`, not a new queue/schema.

The retained window concerns topics, not individual message TTL. Startup and
shared transport loss still reset Sync off under ADR-027/029. Cleanup never
starts, interrupts, or deletes Codex work. Whole-history replay and stronger
per-turn result guarantees across topic expiration are outside this contract.

## Verification

- A 100-chat, multi-page source connects exactly once over bounded batches.
- Definitive failed candidates do not prevent later candidates from connecting.
- An expired passive running topic is deleted and recreated once after fresh
  source activity, without `turn/start` or `turn/interrupt`.
- Existing local writer-state retention protection stays covered.
- An ambiguous activation/discovery create is not retried on later passes or
  after reopening the durable store within the same Sync session.
- First catch-up omits a pre-activation Final; later new results deliver once.
- A failed catch-up Status retries without replaying the absorbed Final; queued
  snapshots from an earlier Sync session cannot populate a new presentation.
- Ready-draft deletion remains asynchronous; failed cleanup stays durable.
