# ADR-036: Best-effort Sync Recovery

External execution and explicit Telegram Retry are amended by
[ADR-037](ADR-037-independent-external-execution.md).
Status: implemented and deployed locally; live service-event reception, ambiguity
handling and late-confirmation cleanup verified. Full off/on UI acceptance remains pending.

## Decision

Telegram is a disposable projection of Codex chats. HTTP Bot API remains the
transport; topic titles contain no technical identifiers. A durable creation
intent precedes every discovery, project-draft and external-launch creation.
Unknown outcomes suppress repetition of that target, while other targets proceed.

The API response confirms its own operation. A bot-authored `forum_topic_created`
message can recover a missing response only when its exact submitted name is
unambiguous across persisted operations and events. Confirmed topic identities
are never replaced by name. Legacy unknown markers without the original name
remain conservative ambiguity fences. Conflicting/unmatched topics are reported
in Control and are not automatically deleted.

Creation confirmation and presentation materialization are transactional and
idempotent, including confirmations arriving after a draft was consumed. Late
confirmations from an ended session become cleanup targets. External drafts are
reserved until the originating request claims its initial message; ordinary
Telegram input cannot race that launch.

Admitted Telegram ingress is durable before polling acknowledgement. One worker
handles normal commands; off bypasses a slow command, invalidates unsent older
inbox work, and fences older activations by update order. Processing commands
with uncertain completion after restart are not replayed: their underlying
receipts/intents reconcile separately and Control receives an uncertainty notice.
Processed payloads are erased; update identities remain as replay tombstones.

`sync off` closes presentation routes and schedules deletion of known managed
session topics, preserving Control and Requests. It never interrupts a turn,
automatically answers a pending request, or closes a writer needed by ongoing
work. `--force` is an alias. Retained execution rows and writer leases reconcile
authoritative terminal evidence independently of the current Sync session.
Unconfirmed dispatches cannot be released because an older terminal turn was read.
Cleanup retries while off, including after restart. Legacy draining becomes off.

Mutating App Server frames are admitted under the lifecycle lock; waiting for
start/steer replies releases it. Lazy initialization retains its existing
serialization. External dispatch releases both lifecycle and launch gates in
reverse order and reacquires them in the declared order; a per-request guard
prevents duplicate workers while those locks are released.

## Boundaries

Bot API cannot enumerate all topics. If both response and event are lost, off/on
can delete only known topics. The operator may delete unknown leftovers manually.
There is no topic pool, MTProto dependency or automatic speculative duplicate
cleanup. Existing selection, retention, rate limits and snapshot rendering remain.

This ADR supersedes the global unknown-create pause in ADR-033, refusal/drain
semantics from ADR-024, and conflicting lifecycle statements in ADR-026/027/032/034.
See the [specification](../process/sync-best-effort-recovery-spec.md) and
[implementation validation](../process/sync-best-effort-recovery-validation.md).
