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
attempts keep independent fingerprints. A new Codex-origin turn is not presented
until its authoritative User item is available and successfully delivered;
Telegram-origin turns may proceed immediately because their original Telegram
message is already visible. This semantic barrier runs before transport priority,
so the visible order starts with User even when `turn/started` is readable before
the user item. For an existing Status anchor, Final is sent before the terminal
Status edit; the edit does not change topic ordering. When a terminal Status must
be created, it is attempted before Final, and a failed creation is not appended
after an already delivered Final.

Status keeps commentary-block duration and renders only the authoritative tool
count in each owning commentary-block header. Individual tool labels and tool
output remain excluded. Counts are computed before compact detail truncation and
merged monotonically within a turn.

Commentary and plan blocks are retained for the whole latest turn. The compact
64-detail tail applies only to other non-output details, so tool activity cannot
evict a Status block or its inferred start time. Total compact projection size
can grow with the number of Status blocks; Telegram text is bounded separately.
Same-turn detail merging uses the current source order, inserting previously
observed but missing runs next to shared identities. Full reads repair legacy
scrambled order. IDs identify blocks; a positive turn-global commentary index
is the fallback for ID-less blocks and supplies their displayed number.

Healthy block starts survive text changes and persisted JSON reloads. Legacy
decreasing block indices, reversed starts, or starts outside the turn interval
trigger one deterministic reconstruction of the inferred timeline. Lost exact
timestamps cannot be recovered. The repaired estimates are saved and remain
stable on later reads. Recovery occurs on the next full read; fully delivered
terminal topics are not force-read solely to migrate their history.
Incomplete observations retain prior items and turn-global indices; arbitrary
partial wire histories that locally renumber commentary are not supported.
If an incomplete read still leaves decreasing block indices, timing recovery
waits for a read that restores the order instead of repeating on every poll.

Active and waiting Status cards also show the local time of the latest successful
bounded snapshot read and the latest valid per-thread App Server event. Event
activity is ephemeral in-memory diagnostic state: streaming events update that
timestamp without triggering a read or SQLite write, and the value resets on a
new Sync activation. Terminal cards omit freshness because their projection is
frozen.

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
- A `turn/started` snapshot without its Codex-origin User item produces no turn
  presentation; the later complete snapshot produces User before Status and Final.
- Final delivery proceeds when terminal Status editing fails and remains
  idempotent on retry.
- Each commentary block shows its complete monotonic tool count without labels
  or output, including when compact detail history is truncated.
- Tool-heavy turns and more than 64 Status blocks retain stable source order,
  global numbering, inferred starts, and the same Status message anchor across
  repeated reads. Legacy scrambled snapshots recover once without losing
  healthy timing anchors when only a prefix was omitted.
- Active freshness distinguishes the latest successful snapshot read from the
  latest valid App Server event without turning streaming deltas into reads or
  durable writes.
