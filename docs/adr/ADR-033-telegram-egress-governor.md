# ADR-033: One Telegram Egress Governor

Status: accepted

## Context

Sync reconciliation, launch-card rendering, terminal delivery, and operator callbacks can all call the Telegram Bot API concurrently. Topic creation during `/sync on` can therefore collide with background edits and produce `429 Too Many Requests`. A launch request that receives `429` from `createForumTopic` must remain recoverable instead of disappearing behind an in-memory retry.

## Decision

The process owns one in-memory Telegram egress governor for every Sync-group write. Each raw Bot API attempt, including every text chunk and entity-free fallback, enters the governor separately.

- Group writes are paced at no more than one attempt per second and no more than 20 attempts in any rolling 60.25-second window. The extra 250 milliseconds avoids relying on an exact server-window boundary while sustaining about 19.9 attempts per minute under continuous load. A fresh governor can send its first 20 attempts one second apart, then waits until the oldest attempt leaves the rolling window.
- Background writes have an additional non-borrowing cap of 12 attempts in the same rolling window. Foreground writes remain subject only to the shared 20-attempt cap, so maintenance cannot consume the whole local budget.
- Foreground and background are the only queued priority classes. After four foreground attempts, one ready background attempt is selected so maintenance cannot starve.
- Every raw queued attempt emits bounded operation, priority, queue-wait, API-duration, and outcome fields to the daemon log. Operation names are fixed by the Telegram adapter and never contain message text or identifiers.
- `answerCallbackQuery` is critical control traffic. Admission-valid callbacks in each received update batch are acknowledged once with neutral text before sequential business handling. It bypasses group pacing and cooldown, but any `429` it observes still extends the shared cooldown. Out-of-scope callbacks remain unanswered.
- A Telegram `retry_after` pauses every noncritical write until that time. The cooldown is intentionally not persisted across process restart.
- Retry timing belongs to the queued egress operation. Ordinary group writes retry their own `429`; launch `createForumTopic` records the cooldown and returns the retryable failure after one attempt so the durable launch card can expose Retry.
- Existing durable fingerprints, rendered-status fields, delivery event ids, and the single launch render owner remain the deduplication mechanisms. The governor does not add a generic coalescing layer.

Launch-card rendering, external-source reply delivery, and Telegram delivery use independent loops. Producers only signal the buffered launch-render wake channel; they do not render inline. This keeps callback acknowledgement independent from slow Telegram writes.

During `/sync on`, all pages of candidate Codex threads are loaded and ordered by work importance before the configured topic limit is applied: waiting/running, unknown nonterminal, failed/interrupted, then completed. When the list reports a thread as `notLoaded`, its last turn supplies the effective running or terminal status. Each class is ordered by most recent update and then thread id.

Topic creation stops after the first ambiguous outcome, such as a timeout or transport EOF. Telegram may have created that topic without returning its id, so automatically continuing or retrying could multiply empty orphan topics. The remaining activation candidates are recorded as skipped. A session-local
`daemon_state` marker is persisted before remote creation and remains until the
binding is durable or failure is definitive. Discovery also stops while that
marker has no known binding; subsequent polls never retry the unknown creation.
Control/status explain the pause and instruct the operator to inspect untracked
topics before explicitly cycling Sync. A marker with an already persisted binding
is safely cleared after an interrupted local completion. The activation state and a `sync_activation` Control delivery are committed in one SQLite transaction; the shared delivery queue retries the summary independently of the command request.

## Consequences

- Telegram group traffic has one rate and cooldown authority without new configuration, dependencies, or database schema.
- Background work can consume at most 12 shared-window attempts, leaving capacity for up to eight later foreground attempts; foreground traffic may still use all 20 slots itself.
- Important launches and user-visible results can pass background status/health maintenance, while background work still progresses.
- A process restart forgets both the local rolling-window history and Telegram's previous `retry_after`; a subsequent `429` immediately rebuilds the cooldown.
- A launch topic `429` becomes a durable failed request with Retry/Close controls rather than an invisible in-memory wait.
- An ambiguous Sync topic creation can leave at most one new untracked topic per
  Sync session; activation and discovery pause further automatic topic creation.
  A new session is an explicit operator recovery boundary after topic inspection.
- The operator receives the activation result through durable Control delivery even if the original Telegram update can no longer send a direct response.
- Precise callback-result toasts are replaced by an early neutral acknowledgement; the durable card edit or scoped failure message carries the result.
- Sync reconciliation still serializes some Telegram work under `syncMu`; removing that lock from I/O would require a separate generation/commit protocol and is not part of this change.

## Verification

- Governor tests cover one-second raw-attempt spacing, the rolling 20-attempt limit, the rolling 12-attempt background cap with foreground passage, bounded foreground preference, per-operation wait observations, shared `retry_after`, critical callback bypass, and return-on-429 behavior.
- Bot tests cover multi-chunk pacing and retryable single-attempt launch topic creation.
- Daemon tests cover activation ordering, retryable launch cards, and callbacks that do not wait for the launch renderer.
- Sync activation tests cover fail-fast ambiguous creation, skipped candidates, atomic summary enqueue, and durable Control delivery.
