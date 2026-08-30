# Quickstart

## 1. Prepare Telegram

Create a bot with BotFather and keep the token private. Create one private forum supergroup containing only you and the bot. Make the bot an administrator with permission to manage topics and delete messages.

Record your numeric user id and the forum supergroup id.

## 2. Install and initialize

On macOS:

```powershell
ctr-go service install --start --start-at-login
ctr-go doctor
```

For a manual installation:

```powershell
ctr-go init
ctr-go doctor
ctr-go daemon run
```

The wizard writes `~/.codex-tg/config.env`. It requires a bot token, exactly one allowed user id, and the Sync group id. `CTR_GO_CONFIG` selects another config path; explicit environment variables override file values.

Environment-only example:

```powershell
$env:CTR_GO_TELEGRAM_BOT_TOKEN = "<telegram-bot-token>"
$env:CTR_GO_ALLOWED_USER_IDS = "<one-telegram-user-id>"
$env:CTR_GO_SYNC_GROUP_ID = "<private-forum-supergroup-id>"
$env:CTR_GO_DEFAULT_CWD = "C:\Users\you\Projects\Codex"
$env:CTR_GO_CODEX_CHATS_ROOT = "C:\Users\you\Documents\Codex"
```

## 3. Enable synchronization

In the forum's Control topic:

```text
/sync on
/status
/projects
```

The bot command menu is scoped only to this forum. `/sync on` materializes recent Codex chats and discovers later chats. Send text in a task topic to start or steer work; use `/stop` there to interrupt the active turn.

`/projects` creates a topic draft from an existing known workspace. `/newchat` creates a dated Codex Chat. Telegram does not accept arbitrary filesystem paths.

After a daemon restart, Sync is intentionally off. Re-enable it after App Server reconnects.

## 4. Check requests and pollers

```text
/pollers
/requests
```

`/pollers` shows which optional sources are enabled and whether they are actively succeeding. `/requests` lists active launch requests and their current state.
