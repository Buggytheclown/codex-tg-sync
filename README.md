# codex-tg-sync

`codex-tg-sync` mirrors local Codex chats into one private Telegram forum with topics. Each Codex thread gets its own topic where the operator can follow status, steer work, answer input, approve commands, stop a turn, and read the final result.

The only supported Telegram surface is Sync mode in that configured forum group. Direct messages, observer commands, and global observer panels are not supported.

The repository is named `codex-tg-sync`; the command-line binary and local data directory keep their existing names: `ctr-go` and `~/.codex-tg`.

## What it does

- Synchronizes Codex Desktop chats into Telegram topics with `/sync on`.
- Starts or steers the correct Codex turn from plain text in its topic.
- Renders compact `[User]`, `[Status]`, `[Approval]`, `[Input]`, and `[Final]` cards.
- Edits resolved approval/input cards in place and removes their buttons.
- Creates project-backed topic drafts with `/projects` and dated chats with `/newchat`.
- Collects durable launch requests from cron, Yandex Messenger, assigned Arcanum reviews, and feedback on the operator's PRs.
- Shows active launch requests with `/requests` and poller state with `/pollers`.
- Sends short terminal notifications back to configured external sources.
- Keeps Codex App Server local; no public App Server listener is required.
- Stores topic identity, callbacks, launch requests, delivery state, and snapshots in local SQLite.

This fork is currently distributed from source. It does not yet publish GitHub Release binaries.

## Requirements

- Git and Go `1.26` or newer.
- OpenAI Codex CLI, signed in, with `codex app-server` available.
- A Telegram bot token.
- One Telegram numeric user id.
- One private Telegram supergroup with Topics enabled, containing only that user and the bot.
- The bot must be an administrator with topic-management and message-deletion rights.

[Codex App Server](https://developers.openai.com/codex/app-server) is currently experimental. Keep it local; do not expose it directly to the internet.

## Quickstart

The commands below are for macOS and Linux. A Windows foreground-run example follows.

### 1. Prepare Telegram

1. Create a bot with [BotFather](https://t.me/BotFather) and save its token.
2. Create a private supergroup and enable Topics.
3. Add only yourself and the bot to the group.
4. Make the bot an administrator with permission to manage topics and delete messages.
5. Record your positive numeric user id and the negative forum group id, which normally starts with `-100`.

### 2. Check Codex

Install and sign in to the [OpenAI Codex CLI](https://developers.openai.com/codex/cli), then verify that App Server is available:

```bash
codex --version
codex app-server --help
```

### 3. Download and build

```bash
git clone https://github.com/Buggytheclown/codex-tg-sync.git
cd codex-tg-sync
go build -buildvcs=false -o ctr-go ./cmd/ctr-go
```

### 4. Configure and run

On macOS, the simplest persistent setup is a user LaunchAgent:

```bash
./ctr-go service install --start --start-at-login
./ctr-go doctor
```

The six-step wizard asks for the bot token, user id, forum group id, default work directory, Codex Chats directory, and Codex binary. It writes `~/.codex-tg/config.env`, installs the LaunchAgent, and starts the daemon. Keep the cloned directory in place because the service points to the built `ctr-go` binary there.

To run in the foreground on macOS or Linux instead, use this path instead of `service install`:

```bash
./ctr-go init
./ctr-go doctor
./ctr-go daemon run
```

On Windows, build and run in PowerShell:

```powershell
git clone https://github.com/Buggytheclown/codex-tg-sync.git
cd codex-tg-sync
go build -buildvcs=false -o ctr-go.exe ./cmd/ctr-go
.\ctr-go.exe init
.\ctr-go.exe doctor
.\ctr-go.exe daemon run
```

### 5. Enable Sync

For the first activation, send these commands in the forum's built-in `General` topic. A successful `/sync on` renames it to `Control`:

```text
/sync on
/status
/projects
```

`/sync on` validates the private two-member forum and connects Codex chats active in the last 24 hours. The initial limit (default five) also controls creation attempts per discovery pass; all fresh chats eventually connect, with no overall count cap. Sync stays on when no chat is fresh. Passive topics expire after 24 hours without activity and return when the chat becomes active again; history stays in Codex. Local unfinished Telegram dispatch stays protected. First catch-up shows the latest prompt/status without notifying a completed pre-activation Final. Send text in a task topic to start or steer its turn; use `/stop` to interrupt it.

If topic creation has an unknown outcome, automatic creation pauses. Control and `/status` identify the ambiguous operation. Existing topics continue updating; inspect the operation before manually clearing its creation marker.

Sync preserves its enabled state, session, topics, and messages after a daemon restart. Catch-up runs automatically after Telegram validation and App Server reconnect. Previous writer leases and approval/input buttons expire; prompts are never replayed.

## First Telegram session

The bot clears its default command scope and publishes commands only in the configured forum group. Use `Control` for group-level commands and task topics for plain-text Codex prompts.

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

Messages, callbacks, and commands outside the configured Sync group are ignored before route or App Server access.

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

PR and cron sources live in the local task board's SQLite database. Existing cron schedules from `~/.codex-tg/cron.json` are imported once; edit them on the board afterward.

Arcanum polling uses the authenticated `ya tool gena-arcanum-cli`. Assigned PRs authored by the configured reviewer are skipped. Sources create one board task per PR cycle, with a copied template and Telegram launch request. New board requests require `Start`; legacy requests retain their previous policy. Moving a task to Polling enables its prepared wait. A matching event returns it to Inbox and can request continuation of the same chat.

## Local task board

The board has a collapsible source list on the left, Inbox / TODO / Polling / Done in the center, and card details on the right. Running/completed describes the chat pass, independently of the column. Launch approval stays in Telegram; discussion and result review stay in Codex.

Build and install the frontend from `ui/`:

```sh
npm ci
npm run build
mkdir -p "$HOME/.codex-tg/ui"
cp -R dist/. "$HOME/.codex-tg/ui/"
```

Set `CTR_GO_CONTROL_API_LISTEN` to a loopback address such as `127.0.0.1:8765`, and `CTR_GO_UI_STATIC_DIR` to the expanded absolute path of `$HOME/.codex-tg/ui` in the daemon's configuration. Restart the daemon and open that address. Keep installed assets in the daemon's application directory: macOS privacy restrictions can prevent a launchd service from reading a checkout under Documents even when API requests work. The static path must not depend on the service's working directory.

After each production UI update, rebuild and copy `dist/.` into the configured static directory again, then refresh the browser. Building alone does not update the installed UI. See [UI run instructions](ui/README.md) for validation and development.

Use templates for initial/continuation prompts and default child waits. Template edits affect future tasks. Moving to TODO/Done cancels unstarted requests; it does not interrupt an active turn. A busy chat requires another explicit Start after it completes. The explicit [codex-task skill](skills/codex-task/SKILL.md) adds the current or linked chat to TODO without starting it.

For UI development, run `npm run dev` in `ui/`; its proxy forwards API requests to the daemon. Configure the explicit loopback `CTR_GO_UI_DEV_ORIGIN` when using a different browser origin. Go changes require a normal rebuild/restart, preserving Sync and tasks; no `/sync off`/`on` cycle is needed. See [ADR-035](docs/adr/ADR-035-local-task-board.md) and the [API contract](docs/process/local-task-board-api.md).

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
- `CTR_GO_SYNC_GROUP_ID`
- `CTR_GO_DEFAULT_CWD`

Common optional settings:

- `CTR_GO_CONFIG`
- `CTR_GO_HOME`
- `CTR_GO_CODEX_BIN`
- `CTR_GO_CODEX_CHATS_ROOT`
- `CTR_GO_APP_SERVER_MODE`
- `CTR_GO_APP_SERVER_SOCKET`
- `CTR_GO_APP_SERVER_LISTEN`
- `CTR_GO_SYNC_INITIAL_TOPIC_LIMIT`
- `CTR_GO_SYNC_POLL_SECONDS`
- `CTR_GO_CONTROL_API_LISTEN`
- `CTR_GO_UI_STATIC_DIR`
- `CTR_GO_UI_DEV_ORIGIN`
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
