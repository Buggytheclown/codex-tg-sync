# Sync Legacy Progress Lifecycle Reuse

## Problem

Sync status delivery has the correct topic-local shape, but it does not yet use
the complete legacy presentation lifecycle. The Sync renderer does not include
observed turn timing, and Telegram-origin Sync turns do not get the short
hot-poll window that compensates for delayed or incomplete App Server live
notifications. A live event currently triggers `thread/read`, but a short
intermediate tool state can still be absent from that read and never reach the
status message.

## Product Contract

- Keep the existing Sync `[Status]` message and notifying `[Final]` message.
- Show the same `Run active for` and `Run duration` footer as legacy mode.
- Refresh active Telegram-origin Sync turns on the same three-second cadence and
  for the same bounded window as legacy mode.
- Treat `thread/read` as the durable source while using the same normalized
  live-tool overlay and preservation rules as legacy mode.
- Continue periodic Sync polling for Desktop-origin turns, including elapsed-only
  edits when no content changed.
- Keep Sync topics, delivery ids, callbacks, writer leases, and routing isolated
  from legacy bindings, panels, observer targets, and the legacy writer.

## Design

The implementation reuses three existing legacy primitives instead of creating
an Sync presentation stack:

1. `appserver.CompactSnapshot` remains the single source of observed
   `LatestTurnStartedAt` and `LatestTurnUpdatedAt`. Sync renders the compacted
   snapshot, not the pre-compaction `thread/read` value.
2. `runTimingFooter` remains the single duration formatter for legacy and Sync.
   The Sync renderer receives an explicit observation time so its fingerprint
   advances while a turn is active.
3. The bounded hot-poll loop is generalized only at the scheduling boundary.
   Legacy and Sync retain separate poll-once functions because their routing,
   terminal ownership, and delivery stores are deliberately different.

For live tool events, Sync normalizes the event before delivery, overlays it onto
the current `thread/read` snapshot with the existing legacy merge rules, and
uses the existing preservation rule on later reads. This prevents a lagging read
from immediately erasing a fresher live tool state.

## Rejected Alternatives

- Reusing legacy `ThreadPanel` and observer routing would violate Sync isolation
  and reintroduce competing lifecycle ownership.
- Copying duration formatting and a separate Sync polling loop would be smaller
  initially but would create two cadence and formatting contracts.
- Polling every Sync topic every three seconds indefinitely would increase load
  and still would not preserve short live-only tool evidence.

## Verification

- Renderer test for active elapsed time and terminal duration.
- Sync polling test proving elapsed-only edits preserve the original turn start.
- Sync hot-poll test proving a targeted active turn refreshes and stops at its
  terminal state.
- Sync live-event test proving a tool missing from the immediate `thread/read`
  still reaches the status message and survives a lagging read.
- Existing stale-turn, terminal-gate, writer ownership, and isolation suites.

