# Telegram UX

The exact configured Sync forum group is the only Telegram surface. Messages and callbacks elsewhere are silently ignored before storage or App Server access.

## Forum layout

- Control is the permanent General topic.
- Each synchronized Codex thread has one task topic.
- Project and Chat creation first creates a durable topic draft.
- A manually created Requests topic hosts external `[Launch request]` cards when external adapters are enabled.

## Task topic chronology

A topic uses compact state cards:

```text
👤 [User]
⏱ [Status]
🔐 [Approval]
❓ [Input]
✅ [Final]
```

The status follows current App Server state. A terminal result produces `[Final]`; long finals are split safely and numbered `Final 1/N` through `Final N/N`.

## Approvals and input

Action callbacks are guarded by group, topic, message, thread, turn, request id, and writer generation. When an action succeeds, the original card is edited to Approved, Denied, Cancelled, or the selected input result. The buttons disappear.

`Allow command prefix` maps to App Server `acceptForSession` for the proposed command prefix. It does not approve every command in the Codex session.

Telegram starts explicitly request `on-request`, `auto_review`, and `workspace-write` so they do not inherit a read-only/untrusted default.

## Commands

Control exposes `/sync`, `/status`, `/pollers`, `/requests`, `/refresh`, `/projects`, and `/newchat`. `/repair` is supported but hidden from the command menu. `/stop` belongs in a task topic.

## Launch requests

Every external request first appears as a `[Launch request]` card. Manual requests use Start/Dismiss; configured Arcanum authors may auto-start only after that card is visible. Failed starts can be retried or closed. Terminal work updates the durable request and, where supported, sends a short reply to the source.

There are no direct-message observer cards, Details pages, Plan cards, Telegram model/settings menus, or automatic tool/log exports.
