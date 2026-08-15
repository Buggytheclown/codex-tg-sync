# ADR-021: AFC Forum Group Surface

- Status: accepted
- Related: ADR-015, ADR-019, ADR-020

## Context

AFC needs a Telegram surface that shows several Codex threads as independently
unread conversations while the existing bot DM remains the legacy product.
Telegram forum topics in one private supergroup provide that UX and preserve the
same Bot API `chat_id + message_thread_id` routing model already used by the
adapter.

The group contains Codex content, so routing by a configured chat id alone is
not an adequate security gate. Topic cleanup also needs typed failure handling:
a deleted or stale topic is different from a retryable Telegram outage.

## Decision

- AFC targets exactly one configured private forum supergroup.
- The activation probe must confirm:
  - the returned chat id equals the configured id;
  - `type=supergroup`, `is_forum=true`, and no public username;
  - the bot is creator or administrator;
  - an administrator bot has `can_manage_topics` and
    `can_delete_messages`;
  - the configured allowed user is an active human member;
  - member count is exactly two: that user and the bot.
- Bot API forum operations are exposed through a narrow `ForumAPI`: probe,
  create/edit/delete topic, topic-scoped send, edit, and delete message.
- Telegram API failures carry method, API/HTTP status, description, and
  `retry_after` when supplied.
- `message thread not found`, deleted-topic, and closed-topic failures are typed
  as stale topic failures. HTTP/API 5xx and 429 are retryable.
- The built-in General topic is prepared manually as permanent Control. AFC does
  not edit or delete it.

## Consequences

- Security/capability validation can run before AFC creates external state.
- Topic cleanup may treat an already deleted topic as converged while retaining
  retry behavior for transient errors.
- Exact configured group updates can be routed before legacy handlers without
  making Telegram ids part of Codex identity.

## Non-goals

- Public groups, multiple AFC groups, or multiple human members.
- Listing or adopting arbitrary existing forum topics.
- Editing the built-in General/Control topic.
- Treating a successful Bot API send as full Telegram live validation.
