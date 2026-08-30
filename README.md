# codex-tg: local Codex control plane

`codex-tg` mirrors local Codex chats into one private Telegram forum. Each Codex thread has one topic where the operator can follow status, steer work, answer input, approve commands, stop a turn, and read the final result.

The supported Telegram surface is AFC/Sync mode only. Direct-message observer commands and global observer panels are not supported.

## What it does

- Synchronizes Codex Desktop chats into Telegram topics with `/sync on`.
- Starts or steers the correct Codex turn from plain text in its topic.
- Renders compact `[User]`, `[Status]`, `[Approval]`, `[Input]`, and `[Final]` cards.
- Edits resolved approval/input cards in place and removes their buttons.
- Creates project-backed topic drafts with `/projects` and dated chats with `/newchat`.
- Collects durable launch requests from cron, Yandex Messenger, and assigned Arcanum reviews.
- Shows active launch requests with `/requests` and poller state with `/pollers`.
- Sends short terminal notifications back to configured external sources.
- Keeps Codex App Server local; no public App Server listener is required.
- Stores topic identity, callbacks, launch requests, delivery state, and snapshots in local SQLite.

Current release: `v0.5.0`.

## Requirements

- OpenAI Codex CLI with `codex app-server`.
- A Telegram bot token.
- One Telegram numeric user id.
- One private forum supergroup containing only that user and the bot.
- The bot must be an administrator with topic-management and message-deletion rights.

## Quickstart

On macOS, install the latest package from [GitHub Releases](https://github.com/mideco-tech/codex-tg/releases/latest), then run:

```powershell
ctr-go service install --start --start-at-login
ctr-go doctor
```

The wizard asks for the bot token, one allowed user id, the AFC forum group id, and local Codex paths. It writes `~/.codex-tg/config.env`, installs a user LaunchAgent, and starts the daemon.

For a manual installation:

```powershell
ctr-go init
ctr-go doctor
ctr-go daemon run
```

Build from source:

```powershell
git clone https://github.com/mideco-tech/codex-tg.git
cd codex-tg
go run ./cmd/ctr-go init
go run ./cmd/ctr-go doctor
go run ./cmd/ctr-go daemon run
```

Environment-only setup:

```powershell
$env:CTR_GO_TELEGRAM_BOT_TOKEN = "<telegram-bot-token>"
$env:CTR_GO_ALLOWED_USER_IDS = "<one-telegram-user-id>"
$env:CTR_GO_AFC_GROUP_ID = "<private-forum-supergroup-id>"
$env:CTR_GO_DEFAULT_CWD = "C:\Users\you\Projects\Codex"
```

Exactly one user id is required. The group id and user id are checked at startup.

## First Telegram session

The bot clears its default command scope and publishes commands only in the configured forum group.

In the Control topic:

```text
/sync on
/status
/projects
```

`/sync on` creates topics for recent Codex chats and keeps discovering new ones. Send text inside a task topic to start or steer its turn. Use `/stop` in that topic to interrupt it.

Sync intentionally resets to off after a daemon restart. Run `/sync on` again after the shared App Server connection is healthy.

## Telegram commands

The visible menu contains exactly:

- `/sync on|off`
- `/status`
- `/pollers`
- `/requests`
- `/refresh`
- `/projects`
- `/newchat`
- `/stop`

`/repair` is accepted in Control but intentionally omitted from the menu. It recreates bridge App Server sessions without deleting Codex threads.

`/projects` lists known local project workspaces. Choosing a project creates a ready topic; the first text message creates the Codex thread and first turn. `/newchat` creates a dated Codex Chat under `CTR_GO_CODEX_CHATS_ROOT`.

Messages, callbacks, and commands outside the configured AFC group are ignored before route or App Server access.

## Permissions and approvals

Telegram-started threads and turns explicitly request:

- approval policy: `on-request`
- approvals reviewer: `auto_review`
- sandbox mode: `workspace-write`

This avoids inheriting a read-only/untrusted App Server default. Codex may still ask for approval. The action card offers one-time approval, command-prefix approval, deny, and cancel. `Allow command prefix` is App Server's `acceptForSession` decision for the proposed prefix; it is not blanket approval for every command in the session.

External launch requests may use explicit execution settings from:

- `CTR_GO_EXTERNAL_REQUEST_APPROVAL_POLICY`
- `CTR_GO_EXTERNAL_REQUEST_APPROVALS_REVIEWER`
- `CTR_GO_EXTERNAL_REQUEST_SANDBOX_MODE`

## Launch requests and pollers

All sources enter the same durable request lifecycle. The Requests topic shows a `[Launch request]` card before a request starts. Use:

- `/requests` for active, non-closed requests and their status.
- `/pollers` for enabled pollers, last attempt/success, consecutive failures, and current activity.

Cron schedules live in `~/.codex-tg/cron.json`.

Assigned Arcanum review polling uses the authenticated `ya tool gena-arcanum-cli`. Authors in `CTR_GO_ARCANUM_REVIEW_AUTO_START_AUTHORS` auto-start after their visible request card is created; other authors require `Start`.

Yandex Messenger requests can require Telegram approval or auto-start according to configuration. When a launched turn reaches a terminal state, the adapter sends a short terminal reply back to the originating Messenger thread.

## App Server modes

- `spawned`: `codex-tg` owns a local stdio App Server.
- `daemon`: connect to the managed local daemon Unix socket.
- `websocket`: connect to an existing loopback WebSocket listener.

Shared modes fail closed and never silently spawn a private App Server. Do not expose App Server publicly.

## Primary configuration

Required:

- `CTR_GO_TELEGRAM_BOT_TOKEN`
- `CTR_GO_ALLOWED_USER_IDS` — exactly one id
- `CTR_GO_AFC_GROUP_ID`
- `CTR_GO_DEFAULT_CWD`

Common optional settings:

- `CTR_GO_CONFIG`
- `CTR_GO_HOME`
- `CTR_GO_CODEX_BIN`
- `CTR_GO_CODEX_CHATS_ROOT`
- `CTR_GO_APP_SERVER_MODE`
- `CTR_GO_APP_SERVER_SOCKET`
- `CTR_GO_APP_SERVER_LISTEN`
- `CTR_GO_AFC_INITIAL_TOPIC_LIMIT`
- `CTR_GO_SYNC_POLL_SECONDS`
- `CTR_GO_CONTROL_API_LISTEN`
- `CTR_GO_LOG_ENABLED`
- `CTR_GO_DIAGNOSTIC_LOGS`

See [`.env.example`](.env.example) for external adapters.

## Runtime commands

```powershell
ctr-go init
ctr-go doctor
ctr-go status
ctr-go repair
ctr-go daemon run
ctr-go service install
ctr-go service start
ctr-go service stop
ctr-go service restart
ctr-go service status
```

A Telegram `409 Conflict` means another process is polling the same bot token.

## Development

Source builds require the Go version declared in `go.mod`.

```powershell
go test ./...
go build -buildvcs=false ./...
```

Architecture and contracts:

- [Sync-only Telegram ADR](docs/adr/ADR-032-sync-only-telegram-surface.md)
- [Telegram contract matrix](docs/research/contract-matrix.md)
- [Regression map](docs/testing/regression-map.md)
- [Architecture](docs/wiki/Architecture.md)
- [Operations](docs/wiki/Operations.md)

## Security

Tokens, user ids, chat ids, SQLite databases, logs, local sessions, private paths, and screenshots are local secrets. Do not commit them. The optional local control API accepts loopback listeners only.
