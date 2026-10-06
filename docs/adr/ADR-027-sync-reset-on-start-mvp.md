# ADR-027: Sync Resume After Bridge Restart

- Status: accepted; amended 2026-10-06
- Amends: ADR-024, ADR-026 restart recovery
- Related: ADR-032, ADR-034

## Context

The original MVP reset Sync to off and made all old topics cleanup targets.
This avoided stale local ownership blocking `/sync off`, but deleted the
operator's Telegram presentation on every rebuild or transport failure.
Keeping topics requires separating durable enabled state from process authority.

## Decision

- SQLite `sync_state.state` remains the source of enabled state. Startup and
  shared transport recovery preserve active/off/draining, session ID, activation
  timestamp, topic bindings, ready drafts, snapshots, Status anchors, delivery
  fingerprints, and pending User correlation.
- In one transaction, accepted receipts become unknown, starting/active local
  ownership becomes unknown with generation zero, starting drafts become unknown,
  and all previous Sync callback routes expire. Successful receipts and external
  launch callbacks keep their separate lifecycle. No operation is replayed.
- Interrupted activation becomes active in the same session. Discovery can add
  missing bindings, but an unresolved creation marker prevents a repeated remote
  topic creation. Existing bindings continue updating despite that marker.
- Startup stays non-blocking. After Telegram is ready, validate the saved group
  and Control before session ingress/egress. Failed verification keeps Sync
  enabled but degraded; a different configured group cannot use that session.
- The existing poll session reconnects and restores generation-scoped
  subscriptions. Reconciliation first reads saved topics, then discovers new
  chats. No extra polling loop or activation is introduced.
- A fresh authoritative read clears detached unknown local ownership by CAS.
  An active Codex turn remains passively active in its snapshot. This transition
  does not dispatch, steer, interrupt, approve, or rewrite an unknown receipt.
  A failed read blocks only that topic's recovery; its cached state cannot cause
  retention deletion until a fresh read succeeds.
- Runtime recovery invalidates old events/leases and closes the old writer.
  Unconfirmed close keeps mutations unavailable; passive reads continue. New
  work is allowed only in active Sync. Draining observes completion and finishes
  already requested off without repeating interrupt. Off stays off.
- Best-effort expired approval/input card edits remove old buttons; route expiry
  remains authoritative if Telegram editing fails. Server requests are not
  automatically rebound to a replacement connection.
- Explicit off and normal retention remain the only sources of cleanup targets.
  Existing historical cleanup rows remain retryable; Control is never deleted.

## Consequences and limits

The same database resumes the same Telegram session. Existing Status cards edit
in place, and the latest undelivered downtime Final uses the existing fingerprint.
The activation boundary never moves on restart. Latest-turn reads cannot promise
every intermediate downtime Final or exactly-once delivery across an unrecorded
Telegram success. The normal reconciliation interval and Telegram governor apply.

Shared daemon runtime is separate: recovery sends no interrupt, but the server's
behavior for a request owned by a closed writer connection requires live checks.
Spawned mode still owns its server process; preserving Telegram presentation does
not guarantee a running turn survives server shutdown. Group/endpoint migration,
unknown thread-create recovery, and restoring deleted topics are out of scope.

## Verification

Storage and daemon recovery suites cover preserved presentation, unknown
operations, repeated recovery, repeated callback generations, detached terminal/
active/superseding turns, failed-read retention protection, group/Control gates,
and draining without replay. Deployment QA compares SQLite and Telegram IDs
before/after bridge-only restart and reads back Control/topic behavior.
