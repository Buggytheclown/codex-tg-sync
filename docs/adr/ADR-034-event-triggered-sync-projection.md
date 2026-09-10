# ADR-034: Event-triggered Sync projection

Status: accepted

## Context

Sync previously combined three state producers: full or summary polling reads,
partial mutations derived from App Server notifications, and queued copies of
the resulting snapshots. A late partial `active` observation could overwrite a
terminal turn, while queued snapshots could deliver that stale state after a
newer Final. Frequent reads of every tracked thread also caused unnecessary CPU,
disk, and network activity.

## Decision

App Server remains the only authority for thread and turn state. Notifications
do not mutate the persisted projection. Important notifications only mark their
thread dirty: turn lifecycle, thread status, item start, and item completion.
Streaming item updates and deltas do not trigger reads.

A single dirty-thread worker coalesces notifications for 500 milliseconds and
performs one bounded latest-turn read per dirty thread. A Telegram-owned active
turn reads through its writer lease; other turns use the polling session. An
event received while a read is in progress leaves the thread dirty for another
pass.

`thread/read` without turns plus `thread/turns/list(limit=1, itemsView=full)` is
the required bounded read. Failure of `thread/turns/list` is reported; Sync does
not silently fall back to an unbounded full-history read.

Periodic reconciliation remains as a safety net for startup, reconnect, event
gaps, and missed subscriptions. It probes tracked topics with the summary view,
skips fully delivered stable terminal turns, and loads a full latest turn for
every other topic. Discovery remains part of this infrequent pass.

The persisted per-thread projection is monotonic within a turn. Terminal state
cannot return to active, Final and User content is sticky, and already observed
detail items are upserted rather than erased by an incomplete read.

The in-memory Telegram delivery queue contains only a coalesced thread key.
Workers load the latest persisted projection immediately before rendering, so a
queued active snapshot cannot overtake terminal state. User, Status, and Final
attempts keep independent fingerprints. For an existing Status anchor, Final is
sent before the terminal Status edit; the edit does not change topic ordering.
When a terminal Status must be created, it is attempted before Final, and a
failed creation is not appended after an already delivered Final.

Status keeps commentary-block duration and renders bounded tool-call labels
immediately below the owning commentary block. Tool output remains excluded.

## Consequences

- Normal traffic is proportional to meaningful activity, not tracked-thread
  count multiplied by a short polling interval.
- Snapshot lifecycle has one producer and one terminal fence.
- The existing Telegram foreground/background governor remains transport QoS;
  it is not state authority.
- Progress may lag an important App Server event by roughly half a second, and a
  missed event by up to the safety-reconciliation interval.
- Older App Server versions without latest-turn listing fail visibly instead of
  reintroducing unbounded reads.

## Verification

- Important events invalidate; streaming updates do not.
- Repeated invalidations produce one queued thread read.
- Stable terminal reconciliation skips the full view only after User, Status,
  and Final are current or intentionally absent.
- A terminal projection cannot regress to active.
- Telegram delivery reads the latest persisted projection.
- Final delivery proceeds when terminal Status editing fails and remains
  idempotent on retry.
- Tools render below their commentary block, are bounded, and never include
  output.
