# Telegram Contract Matrix

ADR-032 defines the active Telegram product contract. Historical direct-message observer commands, panels, Details, Plan Mode, and Telegram settings are not supported.

## Admission boundary

| Input | Required chat | Required user | Behavior outside boundary |
| --- | --- | --- | --- |
| Message | exact `CTR_GO_SYNC_GROUP_ID` | the single `CTR_GO_ALLOWED_USER_IDS` value | silently ignore before SQLite/App Server |
| Callback | exact Sync group | the single allowed user | silently ignore before route lookup |
| Delivery | exact Sync group | n/a | supersede unless kind is `health`, `external_terminal`, or `sync_activation` |
| Bot command menu | exact Sync group scope | Telegram handles visibility | default scope is deleted |

Startup fails when the Sync group is missing or the allowed-user list does not contain exactly one id.

## Command surface

The Bot API chat-scoped menu contains exactly:

| Command | Scope | Contract |
| --- | --- | --- |
| `/sync on|off` | Control | enable synchronization or clean managed topics; activation stops after an ambiguous topic-create result and reports durably |
| `/status` | Control | Sync, App Server, delivery, request, and health summary |
| `/pollers` | Control | configured pollers, current activity, last attempt/success, failures |
| `/requests` | Control | active non-closed launch requests |
| `/refresh` | Control | refresh project/thread discovery |
| `/projects` | Control | create project-backed topic drafts |
| `/newchat` | Control | create a dated Codex Chat topic draft |
| `/stop` | task topic | interrupt the authoritative active turn |

`/repair` is accepted in Control but omitted from the menu. Unknown commands and task-topic misuse return a scoped error in the Sync group. There is no command fallback outside the group.

## Topic lifecycle

- Control is the General topic and is never deleted.
- `/sync on` creates topics for the configured number of recent eligible Codex chats.
- `/sync on` paginates the candidate list and selects waiting/running chats first, then unknown nonterminal, failed/interrupted, and completed chats; recency orders each class.
- Active synchronization discovers later chats without replaying earlier prompts.
- One durable Codex `threadId` maps to one managed topic per Sync session.
- A restart resets Sync to off, cleans old managed topics, and preserves Codex work.
- Project/Chat creation first creates a durable topic draft. The first text message claims the draft, creates the thread, and starts one turn.
- Duplicate Telegram message ids are idempotent.
- Plain text in a bound topic steers a genuinely active turn; otherwise it starts a new turn only after authoritative re-read.

## Turn start policy

Normal Telegram starts explicitly set:

| Field | Value |
| --- | --- |
| `approvalPolicy` | `on-request` |
| `approvalsReviewer` | `auto_review` |
| `sandbox` | `workspace-write` |
| model | thread-preferred model when present, otherwise App Server default |
| reasoning effort | App Server default |

Retired SQLite keys such as `codex.model` and `codex.reasoning_effort` do not affect Telegram starts. External launch requests may provide their own explicit execution settings.

## Message lifecycle

| Card | Creation | Update |
| --- | --- | --- |
| `[User]` | accepted topic prompt/steer | not used as routing authority |
| `[Status]` | active turn | one stable message per turn, edited from current App Server state; live timers advance in ten-second buckets while content/state changes remain immediate |
| `[Approval]` | actionable daemon-owned server request | same card becomes Approved, Denied, or Cancelled; buttons disappear |
| `[Input]` | actionable structured input | same card becomes terminal after answer |
| `[Final]` | authoritative terminal result | long results split safely; multipart chunks are numbered `Final 1/N` through `Final N/N` |
| `[Launch request]` | durable request ingestion | edited across pending, starting, active, failed, closed |

A terminal delivery updates the turn's existing `[Status]` card before sending `[Final]`. Both operations use the same per-topic sequence, so `[Final]` remains the last message for that turn even when Telegram background traffic is queued.

No direct-message observer cards, progress trios, Details pages, Plan cards, settings menus, or automatic log/tool exports exist in the active product.

All raw Sync-group Bot API writes share one in-memory egress governor. It permits at most one attempt per second and 20 attempts per rolling 60.25 seconds, gives foreground traffic bounded preference over background maintenance, and pauses noncritical writes for Telegram `retry_after`. Admission-valid callbacks from each received batch get one neutral acknowledgement before business handling and bypass the group gate; out-of-scope callbacks get no response. The rolling history and cooldown are not persisted across restart.

## Approval decisions

| Button | App Server decision | Meaning |
| --- | --- | --- |
| Approve | `accept` | approve this request |
| Allow command prefix | `acceptForSession` | approve the proposed command prefix for this App Server session |
| Deny | `decline` | reject the request |
| Cancel | `cancel` | cancel the request |

Callbacks are valid only for the stored chat/topic/message, thread, turn, request id, and writer generation. Duplicate or stale callbacks fail closed.

## Launch requests

Cron, Yandex Messenger, and Arcanum review pollers insert requests through the same durable store.

- A visible `[Launch request]` card is created before either manual or automatic start.
- `/requests` shows active requests by default.
- Failed starts may be retried or closed.
- A launch `createForumTopic` `429` is returned after one attempt and rendered as a retryable failed card; it is not hidden by an internal retry loop.
- Ambiguous thread creation is never replayed automatically.
- Terminal Codex state closes the request and queues a short terminal reply when the source supports replies.
- Arcanum authors in `CTR_GO_ARCANUM_REVIEW_AUTO_START_AUTHORS` auto-start after card creation; other authors require approval.
- Poller status is durable enough to show enabled state, current work, last attempt/success, and consecutive failures.

## Storage compatibility

Fresh databases do not create:

- `thread_bindings`
- `observer_targets`
- `telegram_message_routes`
- `pending_approvals`
- `thread_panels`
- `chat_steer_state`

Opening an existing database does not drop those tables. Current Sync topics/drafts, callback routes, receipts, external requests, deliveries, threads, snapshots, and daemon state remain supported.
