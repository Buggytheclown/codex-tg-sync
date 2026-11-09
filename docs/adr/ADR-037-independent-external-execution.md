# ADR-037: Independent External Execution And Telegram Recovery

Status: implemented; local validation is recorded in
[the validation report](../process/external-launch-independent-of-telegram-validation.md).
Amends ADR-029, ADR-032 and ADR-036 for accepted external launches.

## Decision

An accepted external request starts directly through the existing shared writer.
Its durable execution journal is `external_launch_requests`, not a Telegram
message receipt. Initial visibility before automatic admission remains required.
After admission, Telegram transport, topic creation, rendering and rate limits
cannot gate Codex dispatch. Existing workspace, permission, busy-target,
user/group and lifecycle admission rules remain in force.

The dispatch phase distinguishes unsent work, a potentially sent thread creation,
a known created thread, a potentially sent turn and a confirmed turn. The phase
is durable before each mutating RPC; known IDs are durable before subsequent
steps. Restart never repeats an uncertain operation. Retry after a known first
turn rejection reuses the created chat. Task moves during an outstanding thread
creation retain its result but cancel the still-unsent first turn.

Requests, task links and writer ownership are independent of topic presence.
The existing producer observes board tasks and external execution targets.
Terminal snapshots and request completion must be durable before releasing
external ownership, whether the topic is connected, detached or absent.
Runtime supersession reconciliation also finishes an older exact request when
a valid later turn proves it ended. Pending approvals remain actionable in Codex.

Telegram is restored by ordinary Sync and its existing delivery workers.
New attempts use `thread:<id>` across discovery and manual recovery. Historical
request-owned aliases normalize to that identity while preserving their original
identity and uncertain outcomes in the audit payload. Materialization reloads
the canonical operation and checks the current binding within its transaction.
Only one topic becomes the active route; other confirmed IDs are reported.

The launch card exposes Codex and Telegram outcomes separately. Its content
fingerprint includes projection state and available actions, not just execution
status. A terminal request can retain a Telegram recovery action. Failed card
updates remain undelivered. Off/activation and startup dirty projection rendering.

Telegram Retry has a separate callback action and never invokes Codex dispatch.
It consumes one current route and expected attempt/delivery version atomically.
It first uses known bindings or unambiguous confirmations. If creation remains
unknown, an explicit Retry may create one successor attempt; the unknown ancestor
is retained. Automatic discovery and restart do not inherit this permission.
All unknown ancestors remain ambiguity evidence for name-based recovery.

This permits an extra Telegram topic after a lost response. Existing Control
notices report uncertainty and discovered duplicates; ambiguous topics are not
automatically deleted. A late response never replaces a confirmed active route.
Known-topic Retry uses existing message delivery and partial-progress fingerprints.
Message delivery still has the existing at-least-once network boundary.

Telegram IO releases the lifecycle lock. Its completion revalidates session and
ownership; attaching a message ID cannot reactivate a callback already resolved
while the send was waiting.

## Compatibility And Limits

Legacy receipt routes remain readable. Only proven old pre-dispatch Telegram
failures become retryable; missing IDs alone do not prove no dispatch occurred.
Migration does not launch jobs. Topic confirmations no longer launch Codex.

Ordinary `/newchat` and `/projects` drafts are unchanged. There is no new queue,
service, transport or dependency. Existing source/reply and task completion
contracts remain in use. Deployment is separate from implementation; an old
binary must not resume a new in-flight journal without checking its phases.
