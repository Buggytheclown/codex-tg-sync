# Sync Vertical Slice Acceptance

The supported Telegram vertical slice is one configured private forum group and one allowed operator.

## Automated acceptance

- Out-of-group messages and callbacks are ignored before storage or App Server access.
- Bot commands are removed from default scope and registered only for the Sync group.
- `/sync on` creates topics for recent eligible Codex chats.
- A topic prompt starts one turn; a duplicate update does not replay it.
- Later topic text steers a genuinely active turn.
- Approval and structured-input callbacks are ownership-guarded and edit the same card to terminal state.
- `/pollers` and `/requests` work through public Control routing.
- Fresh SQLite databases contain no retired Telegram tables; existing databases open non-destructively.
- Startup retires queued out-of-group and observer deliveries while preserving current in-group health, terminal, and Sync activation deliveries.

## Live acceptance

1. Confirm the command menu appears in the Sync group and not in a direct message.
2. Run `/status`, `/pollers`, and `/requests` in Control.
3. Run `/sync on` and confirm a recent Codex chat has exactly one topic.
4. Send a prompt in that topic and confirm `[User]`, active `[Status]`, and `[Final]`.
5. If an approval/input request occurs, resolve it and confirm the same card is edited without buttons.
6. Restart the daemon and confirm Sync returns to off without interrupting authoritative Codex work.
