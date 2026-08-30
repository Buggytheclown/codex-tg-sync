# Sync Aggregate Status Blocks

## Product Contract

Each Sync turn owns one live Status message. The message keeps every textual
commentary/reasoning or plan block observed for that turn in chronological
order. A new block is appended to the rendered aggregate; an update for the
same stable item id edits that block in place. User, Final, tool, and tool-output
items are not Status blocks.

The first line always remains compact and outside any collapsed content so it
is useful in Telegram topic previews:

```text
⏱ [Status] inProgress · 2m 10s
```

The expanded active body is:

```text
Block 1 · 32s
Checking the current architecture…

Block 2 · 1m 05s
Found the cause…

Block 3 · 33s
Running verification…
```

Before the first real commentary or plan block, Status contains only the
header. `Thread.LastPreview` is not a fallback because it can belong to an
older prompt.

## Approximate Block Timing

App Server does not expose timestamps for commentary items. The bridge stores
an effective interval start for each Status block in the compact thread
snapshot. No new SQLite table is required.

- The first block starts at the turn start.
- When a later block appears, the prior block stops and the new block starts.
- The active final block runs until the current observation time.
- At terminal state the final block stops at the frozen turn end.
- Rounded block durations should sum approximately to the overall duration.
- If one poll discovers several new blocks, the unobserved interval is divided
  evenly between the previous current block and the newly discovered blocks.
- On upgrade or recovery without any persisted block timing, the known turn
  interval is divided evenly across the observed blocks.

Stable identity is `kind + item id`. If App Server omits an item id, the bridge
falls back to the block's chronological index. Text updates for the same
identity do not reset timing.

## Telegram Presentation

While a turn is active or waiting, the aggregate body is expanded. At terminal
state, the body is marked with one `expandable_blockquote` Telegram entity. The
compact header remains outside that entity. The Status message is retained
after the separate notifying Final message is sent.

The existing tail-status lifecycle remains authoritative. A same-turn Telegram
acknowledgement or Desktop user mirror can reanchor Status by deleting the old
message and sending one replacement, but there is still only one current
aggregate Status message for the turn.

Status delivery uses a rendered message payload so its fingerprint includes
both text and entities. User, Final, Approval, and Input contracts remain
unchanged.

## Telegram Limit And Trimming

The normal Telegram text-message limit is 4096 characters after entity
parsing. The renderer always preserves the full header and the newest tail of
the body. If the aggregate does not fit, it removes the oldest body lines and
adds one short marker before the retained tail:

```text
… removed lines: 37
```

The marker is localized in the product language as `… удалено строк: N`.
Trimming is presentation-only: full DetailItems and block timing remain in the
compact snapshot. If the latest block alone exceeds the budget, the renderer
keeps its label and newest text tail and counts the removed prefix as at least
one removed line.

## Failure And Recovery

- Telegram edit failure leaves persisted block timing intact and retries the
  same deterministic render on the next reconciliation.
- Restart preserves effective block starts from the compact snapshot.
- A stale poll for an older turn remains ignored by existing active-writer
  guards.
- A new turn receives a fresh Status message and fresh timing.
- Legacy direct-message panels and their Details/tool presentation do not
  change.

## Verification

- Consecutive commentary blocks append in chronological order in one message.
- Same-id commentary updates in place without duplicating or resetting time.
- Plan blocks participate; tool and output items do not.
- Active block durations approximately sum to the overall elapsed time.
- Several blocks first observed in one poll receive a deterministic even timing
  allocation.
- Terminal duration and all block durations remain stable across later polls.
- Terminal body has an `expandable_blockquote` entity and Status remains after
  Final.
- Overflow removes only the oldest body lines, reports the count, and preserves
  the header and latest block tail within the Telegram limit.
- Tail reanchoring, Desktop user mirror, Telegram no-echo, full tests, race
  tests, build, and real Telegram readback pass.
