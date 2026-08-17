# AFC Compact Message Headers

## Product Contract

AFC topic previews must make the bridge message kind and current turn state
readable without opening the topic. AFC adds one stable icon before each
bracketed message-kind label and keeps the existing label text for search,
tests, and operator familiarity:

- `👤 [User]`
- `⏱ [Status]`
- `✅ [Final]`
- `🔐 [Approval]`
- `❓ [Input]`

The icon identifies the message kind. It does not change with turn state, so an
edited live status keeps a stable visual identity.

The AFC status header owns the compact run timing value:

```text
⏱ [Status] inProgress · 59s
⏱ [Status] completed · 15m 04s
```

The verbose `Run active for:` and `Run duration:` footer is removed from AFC
status messages. Detail text starts on the next line. If timing evidence is not
available, the header omits the separator and duration. Legacy direct-message
panels and their timing footer remain unchanged.

## Timing Semantics

Active and waiting turns show elapsed time from the first observation of the
turn through the current render time. Terminal turns show the duration from the
same start through the first terminal observation.

Compact App Server snapshots do not always include turn timestamps. The bridge
already synthesizes them. On a same-turn transition from a live status to a
terminal status, it records the observation time as the end. Later terminal
polls preserve that end instead of advancing it. A new turn receives fresh
timing state.

## Delivery And Compatibility

The change is presentation-only. User fingerprints, status fingerprints,
tail-status reanchoring, Final notification behavior, callback routing, and
Telegram-origin no-echo rules remain unchanged. Existing status messages are
edited into the new format on the next reconciliation because their render
fingerprint changes.

Only AFC group messages adopt these headers. Legacy direct-message cards,
Details exports, and historical documentation contracts are not migrated.

## Verification

- Active AFC status renders the icon, state, and compact elapsed value on the
  first line, with no timing footer.
- Terminal AFC status renders a frozen compact duration on the first line.
- Repeated terminal polls do not edit status solely because time passed.
- User, Final, Approval, and Input messages receive their distinct icons.
- Tail reanchoring and Telegram-origin user no-echo behavior remain intact.
- Full tests, race tests, build, and real Telegram readback pass.
