# AFC User Message Mirror

## Product Contract

Each normalized Desktop-origin Codex user message is represented in its mapped
Telegram topic as a silent message:

```text
[User]
<normalized prompt text>
```

The message is appended once. Repeated polling and daemon restart do not create
duplicates. If a live status for the same turn already exists, the bridge moves
that status back to the topic tail so the visible order remains `User ->
Status -> Final`.

Telegram-origin prompts and steers are not echoed by the bot: the original
Telegram message is already the canonical user presentation. Their ACK and
tail-status lifecycle remains unchanged.

This iteration mirrors the normalized plain-text user content exposed by
`thread/read`. Attachment/media mirroring and historical conversation backfill
are outside this contract.

## Persisted State

`afc_topics` owns three additional delivery fields:

- `last_user_fp`: the latest Codex user item already presented or intentionally
  suppressed because its Telegram original is visible;
- `pending_telegram_user_fp`: a text fingerprint for a successfully dispatched
  Telegram prompt that has not yet appeared in `thread/read`;
- `pending_telegram_turn_id`: the exact turn for that pending fingerprint.

The pending state is written while the AFC presentation lock is still held,
before dispatch returns its ACK. Therefore a passive poll cannot mirror the
same Telegram prompt in the dispatch-to-ACK window.

On upgrade, topics that already have a delivered status adopt the latest user
fingerprint from their stored snapshot. This prevents deployment from appending
old user prompts after existing Final messages. Newly activated or newly
discovered topics still render their current user prompt on first presentation.

## Presentation Flow

For each authoritative snapshot:

1. If the user fingerprint equals `last_user_fp`, do nothing.
2. If a pending Telegram input exists for this turn:
   - matching normalized text consumes the pending state and advances
     `last_user_fp` without sending;
   - a non-matching snapshot is deferred until App Server exposes the dispatched
     input, avoiding a stale or duplicate user message.
3. A Telegram-origin new turn without pending state also advances
   `last_user_fp` without sending.
4. Otherwise send one silent `[User]` message and persist `last_user_fp`.
5. If the prior live status belongs to this same turn, reset and best-effort
   delete it. Normal status delivery then creates a fresh tail anchor.
6. Continue with the existing status and final fingerprint lifecycle.

User delivery failure stops presentation for that snapshot so status/final do
not overtake a missing user message. The next poll retries. Status deletion
remains best-effort and follows the existing tail-status contract.

## Rejected Alternatives

- Suppressing every user item in a Telegram-touched turn loses later Desktop
  user messages in that same turn.
- Suppressing only turns marked Telegram-origin duplicates Telegram steers into
  Desktop-origin turns.
- Matching only the latest text without persisted pending state races with the
  dispatch/ACK window and cannot survive restart.
- Reusing legacy `thread_panels` couples AFC to a lifecycle that AFC deliberately
  does not create.

## Verification

- Desktop new turn: `[User]` is sent before `[Status]`, then `[Final]`.
- Desktop same-turn follow-up: one new `[User]` is appended and the same-turn
  status is reanchored after it.
- Repeated poll/restart: no duplicate `[User]`.
- Telegram new turn and steer: original prompt remains the only user message;
  ACK and status order is unchanged.
- Pending Telegram input prevents a passive-poll race from echoing the prompt.
- Upgrade migration does not backfill user messages into topics with existing
  presentation history.
- Full tests, race tests, build, and real MTProto topic readback pass.
