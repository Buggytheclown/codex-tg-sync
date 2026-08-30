# Create AFC Topic From Project Brief

## Problem

An operator needs to start a new Codex task from Telegram without supplying an arbitrary filesystem path and without losing the durable topic-to-thread mapping.

## Goal

`/projects` and `/newchat` create durable AFC topic drafts. The first plain-text message in the draft atomically claims it, creates the Codex thread in the selected known cwd, starts the first turn, and binds the topic to the returned `threadId`.

## Non-goals

- Creating or discovering arbitrary work directories from Telegram.
- Creating empty Codex threads before the first prompt.
- Restoring direct-message bindings or one-shot chat state.
- Changing the App Server protocol.

## Operator flow

1. Run `/projects` in Control.
2. Choose a known project workspace, or run `/newchat` for a dated Codex Chat.
3. The bot creates a ready forum topic draft.
4. Send the first prompt in that topic.
5. The draft becomes starting, then connected to the App Server-owned thread id.
6. The topic renders current `[User]`, `[Status]`, approval/input, and `[Final]` state.

A second message while creation is in progress is rejected rather than starting another thread. Duplicate Telegram message ids are idempotent.

## Domain model

Project workspaces are derived from cached `threads.cwd` metadata. A topic draft stores AFC session id, exact group/topic ids, rank, title, selected cwd, state, and the first Telegram message id. Current callback routes support project navigation; no generic chat binding or panel state is created.

## Architecture

- Telegram topic creation happens before Codex thread creation.
- SQLite durably records and claims the draft.
- App Server `thread/start` owns thread identity.
- App Server `turn/start` starts the first prompt with the current Telegram permissions.
- The daemon promotes the draft to an AFC topic only after it has the returned thread id.
- Ambiguous `thread/start` outcomes are not replayed automatically.

## Testing

- Project catalog and callback tests cover known-workspace selection.
- Draft tests cover ready/claim/idempotency/concurrent-message behavior.
- Writer tests cover unclaimed process reservation and returned-thread claim.
- Permission tests cover explicit `on-request`, `auto_review`, and `workspace-write`.
- Live QA uses Control -> project selection -> topic draft -> first prompt -> `[Final]`, plus an approval/input callback when available.

## Acceptance criteria

- [ ] `/projects` exposes only cached known workspaces.
- [ ] `/newchat` uses the configured Codex Chats root.
- [ ] A first topic message creates exactly one thread and one turn.
- [ ] A duplicate or concurrent message cannot create a second thread.
- [ ] The topic is bound to the returned durable thread id.
- [ ] Failure remains visible and recoverable without direct-message state.
