# codex-tg: local Codex control plane

Local control plane for OpenAI Codex App Server, built in Go. Its supported
Telegram surface is Sync mode: one private forum mirrors Codex Desktop chats as
topics and keeps prompts, live status, approvals, input requests, and final
answers attached to the correct Codex thread.

## Why codex-tg?

- Mirror Codex Desktop chats into a private Telegram forum with `/sync on`.
- Continue or stop the correct Codex turn from its Telegram topic.
- Approve Codex and scheduled launch requests without exposing App Server to the internet.
- Reuse your existing Codex setup: skills, MCP servers, plugins, repo instructions, and local workflows.
- Run local daily or weekly Codex prompts through the same durable approval flow.
- Durable thread-first routing keeps every topic attached to the right local task.

Current release: `v0.5.0`.

## Why It Matters

- Keep local Codex work observable and controllable without exposing Codex App Server to the internet.
- Use Telegram as a low-friction synchronized control surface for local Codex work.
- Reuse one durable launch path for Telegram, cron, and optional source adapters.
- Preserve local-first ownership: Codex sessions, workspaces, SQLite state, and tokens stay on your machine.

## Remote Connections

Official Codex Remote Connections cover the broad mobile remote-control
workflow for Codex. `codex-tg` is not trying to replace that feature. The
project direction is a local control layer and adapter system. Telegram Sync
mode remains the operator surface, while cron and other private adapters feed
the same durable launch lifecycle.

## Features

- Sync lifecycle through `/sync on|off`, `/status`, `/refresh`, and non-destructive `/repair`.
- One managed Telegram forum topic per synchronized Codex Desktop chat.
- Plain-text topic messages start or steer the authoritative turn; `/stop` interrupts it.
- Compact `[User]`, `[Status]`, `[Approval]`, `[Input]`, and `[Final]` messages.
- Project and Chat creation through `/projects` and `/newchat` in Control.
- Durable external launch requests with Telegram `Start` / `Dismiss` approval.
- Reloadable JSON cron schedules with daily and weekly catch-up without backlog.
- SQLite-backed topic identity, callbacks, launch requests, and delivery state.
- macOS service installer with friendly first-run setup, user LaunchAgent management, and menu bar tray control.
- Cross-platform Go daemon foundation for Windows, macOS, and Linux.

## Platform Status

- Windows: tests and builds are supported; current Sync-mode live validation is macOS-focused.
- macOS: primary service/runtime path, using a user LaunchAgent and the shared managed App Server.
- Linux: CI runs tests/builds on Ubuntu; full local daemon/runtime validation is still pending.

## Quickstart

Prerequisites:

- OpenAI Codex CLI with `codex app-server`.
- A Telegram bot token from BotFather.
- Your Telegram numeric user id.
- A private Telegram forum supergroup containing only you and the bot; the bot
  must be allowed to manage topics and delete messages.

On macOS, download the latest `.pkg` from
[GitHub Releases](https://github.com/mideco-tech/codex-tg/releases/latest),
install it, then run:

```powershell
ctr-go service install --start --start-at-login
ctr-go doctor
```

`ctr-go service install` starts a friendly first-run setup wizard when required.
The same values can be passed with flags for scripted installs. It writes a
private local config file at `~/.codex-tg/config.env` by default, creates a
user LaunchAgent, and starts the daemon when `--start` is present.
If your shell uses proxy variables such as `HTTPS_PROXY` or `NO_PROXY`, the
installer preserves them in the private config so the LaunchAgent can reach the
same network without putting secrets or user ids into the plist.

For Linux, Windows, or manual macOS setup, download the latest `ctr-go` archive,
unpack it, then run:

```powershell
ctr-go init
ctr-go doctor
ctr-go daemon run
```

Use `CTR_GO_CONFIG` to point at another config file. Explicit environment
variables still override config file values.

Build from source:

```powershell
git clone https://github.com/mideco-tech/codex-tg.git
cd codex-tg
go run ./cmd/ctr-go init
go run ./cmd/ctr-go doctor
go run ./cmd/ctr-go daemon run
```

Environment-only setup remains supported:

```powershell
$env:CTR_GO_TELEGRAM_BOT_TOKEN = "<telegram-bot-token>"
$env:CTR_GO_ALLOWED_USER_IDS = "<telegram-user-id>"
$env:CTR_GO_DEFAULT_CWD = "C:\Users\you\Projects\Codex"
```

In Telegram:

```text
/sync on
/status
/projects
```

`/sync on` reconciles recent Codex Desktop chats into forum topics. Send plain
text in a task topic to start or steer its turn; use `/stop` in that topic to
interrupt the active turn. The daemon intentionally resets Sync to `off` after
a restart, so enable it again after the shared App Server is healthy.

## Runtime Commands

```powershell
ctr-go init
ctr-go service install
ctr-go service start
ctr-go service stop
ctr-go service restart
ctr-go service status
ctr-go doctor
ctr-go status
ctr-go repair
ctr-go daemon run
```

Source-build equivalents:

```powershell
go run ./cmd/ctr-go init
go run ./cmd/ctr-go service install
go run ./cmd/ctr-go doctor
go run ./cmd/ctr-go status
go run ./cmd/ctr-go repair
go run ./cmd/ctr-go daemon run
```

Supported Telegram command menu:

- `/sync on|off` controls Sync mode
- `/status`, `/refresh`
- `/projects`, `/newchat`, `/stop`

`/repair` is also accepted in Control and recreates bridge App Server sessions
without deleting Codex threads. Sync mode in the configured private forum is
the only supported Telegram product surface; direct-message command workflows
outside that forum are unsupported.

`/projects` opens cached project/workspace navigation in Control. Choosing a
project creates a ready Telegram draft topic; its first plain-text message
creates the Codex thread and first turn. `/newchat` opens the same project
picker for a dated Codex Chat.

## Configuration

Primary environment variables:

- `CTR_GO_HOME`
- `CTR_GO_CONFIG` (`~/.codex-tg/config.env` by default)
- `CTR_GO_CODEX_BIN`
- `CTR_GO_APP_SERVER_MODE` (`spawned` by default; use `daemon` to connect directly to the managed daemon Unix socket)
- `CTR_GO_APP_SERVER_LISTEN`
- `CTR_GO_APP_SERVER_SOCKET` (optional Unix socket override for daemon mode)
- `CTR_GO_CONTROL_API_LISTEN` (empty/off by default; experimental local router-agent API, loopback TCP only)
- `CTR_GO_TELEGRAM_BOT_TOKEN`
- `CTR_GO_ALLOWED_USER_IDS`
- `CTR_GO_ALLOWED_CHAT_IDS`
- `CTR_GO_AFC_GROUP_ID` (exact private forum supergroup used by Sync mode)
- `CTR_GO_AFC_INITIAL_TOPIC_LIMIT` (`5` by default; initial `/sync on` snapshot only)
- `CTR_GO_YMESSENGER_ENABLED` (`false` by default)
- `CTR_GO_YMESSENGER_ROBOT_LOGIN` (required when the adapter is enabled)
- `CTR_GO_YMESSENGER_OAUTH_TEAM_TOKEN` (required secret robot token)
- `CTR_GO_YMESSENGER_ALLOWED_SENDERS` (comma-separated sender login allowlist)
- `CTR_GO_YMESSENGER_POLL_SECONDS` (`2` by default)
- `CTR_GO_YMESSENGER_REQUIRE_APPROVAL` (`true` by default; set `false` to start allowed explicit mentions automatically)
- `CTR_GO_EXTERNAL_REQUESTS_TOPIC_ID` (permanent Telegram request/status topic id)
- `CTR_GO_EXTERNAL_REQUEST_DEFAULT_CWD` (falls back to `CTR_GO_DEFAULT_CWD`)
- `CTR_GO_EXTERNAL_REQUEST_APPROVAL_POLICY` (`never`, `on-request`, or `untrusted`; empty inherits the App Server default)
- `CTR_GO_EXTERNAL_REQUEST_APPROVALS_REVIEWER` (`user` or `auto_review`; empty inherits the App Server default)
- `CTR_GO_EXTERNAL_REQUEST_SANDBOX_MODE` (`read-only`, `workspace-write`, or `danger-full-access`; empty inherits the App Server default)
- `CTR_GO_DEFAULT_CWD`
- `CTR_GO_CODEX_CHATS_ROOT` (`~/Documents/Codex` by default)
- `CTR_GO_LOG_ENABLED` (`true` by default; set `false`/`off`/`0` to discard daemon stdout logs)
- `CTR_GO_DIAGNOSTIC_LOGS` (`true` by default; set `false`/`off`/`0` to keep normal bot logs but suppress structured `daemon_event` diagnostics)
- `CTR_GO_OBSERVER_POLL_SECONDS`
- `CTR_GO_REQUEST_TIMEOUT_SECONDS`
- `CTR_GO_PROJECTS_PROJECT_PREVIEW_LIMIT` (`7` by default)
- `CTR_GO_PROJECTS_CHAT_PREVIEW_LIMIT` (`3` by default)
- `CTR_GO_CHATS_PAGE_SIZE` (`8` by default)
- `CTR_GO_INDEX_REFRESH_SECONDS`
- `CTR_GO_ATTACH_REFRESH_SECONDS`
- `CTR_GO_DELIVERY_RETRY_SECONDS`
- `CTR_GO_DELIVERY_MAX_ATTEMPTS`

Compatibility fallbacks:

- `CTR_TELEGRAM_BOT_TOKEN`
- `CTR_ALLOWED_USER_IDS`
- `CTR_ALLOWED_CHAT_IDS`

## Telegram Sync mode

Set `CTR_GO_AFC_GROUP_ID` to one private Telegram forum supergroup and configure
exactly one `CTR_GO_ALLOWED_USER_IDS` value. The group must contain only that
user and the bot; the bot must be able to manage topics and delete messages.
Use `/sync on` in the built-in General topic. Activation validates the group and
idempotently renames that built-in topic to `Control` before creating managed topics for
five recent Codex threads by default. While Sync is active, reconciliation creates one
topic for each newly discovered Desktop chat; `/refresh` triggers the same reconciliation
immediately. Control advertises `/projects` and `/newchat`;
either command creates a Telegram draft topic without holding an App Server
writer. Send its first prompt only after the topic is ready: Sync creates the
Codex thread and first turn together, then renames the topic from that prompt.
Messages sent while a topic turn is active steer that exact turn, and `/stop`
targets the current shared-daemon turn regardless of whether Desktop or Telegram
started it. `/sync off` drains safely and removes managed Telegram topics but
does not delete Codex threads. Use `/sync off --force` only when active turns
must be interrupted and drained.

### Yandex Messenger launch requests

The optional Yandex Messenger adapter runs inside the same `codex-tg` daemon;
the normal one-command startup does not change. Approval is secure by default.
When it is enabled, create a permanent `Requests` topic manually in the
configured Sync forum group and put its topic id in
`CTR_GO_EXTERNAL_REQUESTS_TOPIC_ID`. Every accepted request is rendered there
as a durable status card. Set `CTR_GO_YMESSENGER_REQUIRE_APPROVAL=false` to
start allowed explicit mentions automatically; auto-start cards have no
approval buttons and are edited as the launch progresses.

The poller accepts a message only when `from.login` is in
`CTR_GO_YMESSENGER_ALLOWED_SENDERS` and the configured robot is present in
`mentioned_users`. Source `chat_id` is deliberately not filtered. Accepted
messages receive a durable acknowledgement and appear in `Requests`;
approval-gated requests include `Start`
and `Dismiss`, while automatic requests are informational only. Dispatch
requires Sync to be active, creates one normal task topic, then uses the shared
writer to perform `thread/start` and the first `turn/start`.
Optional external-request permission settings are passed explicitly to both
calls, so the launch does not depend on cached App Server defaults.
An explicit robot mention from any other sender receives a static owner-only
reply. It stores no Codex prompt and cannot create a Telegram approval or
Codex session.

For direct replies, the nested `reply_to_message` is included as untrusted
context. For thread messages, the adapter uses the Messenger API invariant that
`chat.thread_id` equals the root message timestamp and resolves that exact
message on demand through Messenger History API. The configured robot OAuth
token is reused with the History API `OAuth` authorization scheme; no user
token or additional secret is required. History lookup runs only for an
allowed explicit robot mention. History authorization failures leave the
cursor unchanged. Other failures for one exact root retry twice, then advance
with an explicit unavailable-context marker so one message cannot block all
later updates. The daemon does not cache unrelated chat messages.

### Cron launch requests

An optional `~/.codex-tg/cron.json` file can enqueue daily or weekly Codex launch
requests through the same durable Telegram approval and Sync dispatch lifecycle.
The daemon reloads the file every 30 seconds; a missing file disables the
source, while invalid JSON or task configuration fails closed for that poll.

The supported five-field subset is deliberately small:

- daily: day-of-month, month, and day-of-week are `*`;
- weekly: day-of-month and month are `*`, and day-of-week is one number or
  three-letter weekday such as `1` or `MON`.

A daily task creates at most one request per local calendar day. A weekly task
creates at most one request per local calendar week and uses the scheduled
weekday's date as its durable identity. Starting after the current period's
slot catches up once; missed days or weeks never create a backlog. After daemon
startup or a sleep-sized polling gap, catch-up waits for four minutes of
continuous runtime so a short laptop DarkWake does not create a request.

```json
{
  "version": 1,
  "timezone": "Europe/Berlin",
  "tasks": [
    {
      "id": "morning-project-brief",
      "cron": "0 10 * * *",
      "cwd": "/path/to/project",
      "prompt": "Prepare the morning engineering brief.",
      "model": "gpt-5.6-luna",
      "reasoning_effort": "high",
      "launch_policy": "telegram",
      "max_lateness": "2h",
      "enabled": true
    },
    {
      "id": "weekly-project-radar",
      "cron": "0 10 * * MON",
      "cwd": "/path/to/another-project",
      "prompt": "Prepare the weekly project radar.",
      "model": "gpt-5.6-sol",
      "reasoning_effort": "high",
      "launch_policy": "telegram",
      "enabled": true
    }
  ]
}
```

`launch_policy` defaults to `telegram`, which requires the configured external
requests topic and presents `Dismiss` / `Start`. Use `auto` for future requests
that should enter the existing auto-start path. Existing pending requests are
not rewritten after configuration changes. Optional `max_lateness` accepts a
positive Go duration such as `30m` or `2h`; an older occurrence is skipped.
Omitting it keeps catch-up available for the full local day or week.

The existing App Server subscription and authoritative `thread/read` snapshot
remain the only source of the Codex final answer. A terminal final is queued in
SQLite and sent through Bot API `sendText` with the source `chat_id`, invoking
`message_id` as `reply_message_id`, and source `thread_id`. The terminal answer
is returned to Messenger; a failed, interrupted, or empty terminal turn gets a
short explicit fallback instead of silence. Commentary and tool output remain
in the normal Sync task topic.

The update cursor and normalized request are committed in one SQLite
transaction. Duplicate source messages and button presses are ignored. A
daemon restart after a dispatch claim, or an ambiguous App Server response,
marks the outcome unknown and never replays it automatically. Telegram
send/edit failures remain pending for reconciliation. Messenger reply state is
visible in `doctor` as `external_reply_backlog` and delivery transitions are
written as structured lifecycle events. OAuthTeam and Telegram tokens stay
only in the private config and are omitted from status/doctor JSON.

### Shared App Server startup on macOS

Sync mode requires Codex Desktop and `codex-tg` to connect to the same
managed App Server. The startup order matters.

#### After every reboot

**Do not open Codex Desktop first.** The `launchctl` environment override and a
manually started managed daemon do not survive a reboot. Use this order:

1. Start the managed App Server daemon.
2. Set local-daemon mode for GUI apps launched afterward.
3. Open Codex Desktop.
4. Start or restart `codex-tg`, verify the shared connection, then run `/sync on`
   in Control.

Run steps 1 and 2 before opening Desktop:

```bash
/Applications/ChatGPT.app/Contents/Resources/codex app-server daemon start
launchctl setenv CODEX_APP_SERVER_USE_LOCAL_DAEMON 1
```

After opening Desktop, verify and restart the bridge if needed:

```bash
/Applications/ChatGPT.app/Contents/Resources/codex app-server daemon version
ctr-go service restart
ctr-go status
```

When installed with `--start-at-login`, `codex-tg` may start automatically
before the managed daemon. In that case it stays fail-closed, resets Sync to
`off`, and sends one warning to Control. It does not spawn a private App Server.
Start the managed daemon, restart Desktop in local-daemon mode, and then run
`/sync on`.

#### If Codex Desktop was opened first

Desktop probably started a private App Server. Recover with this exact order:

1. Fully quit Codex Desktop with **Cmd+Q**; closing its window is not enough.
2. Run the two daemon and `launchctl` commands above.
3. Reopen Codex Desktop.
4. Run `ctr-go service restart`, verify `ctr-go status`, and send `/sync on` in
   Control.

Every `codex-tg` restart intentionally resets Sync to `off`. Existing
Codex work continues in the shared runtime, but the old Telegram topics are
cleaned up. Run `/sync on` in Control after the shared connection is healthy.
The bridge applies the same boundary when its managed-daemon heartbeat detects
a broken or half-open transport: connection status becomes false, Control gets
one warning and one recovery notice, polling reconnects, and Sync remains off
until the operator runs `/sync on` again. Prompts are never replayed.

## Verification

```powershell
go test ./...
go build -buildvcs=false ./...
```

Live Telegram readback E2E is documented in
[tests/live_e2e/README.md](tests/live_e2e/README.md). It is intentionally
gated by local env and is not part of `go test ./...`.

## GitHub Metadata

Suggested repository description:

```text
Sync local Codex Desktop chats with a private Telegram forum; steer, approve, and schedule Codex work without exposing App Server publicly.
```

Suggested topics:

```text
codex telegram telegram-bot telegram-ui openai-codex codex-cli
codex-app-server codex-control-plane ai-agents coding-agent remote-control developer-tools
local-first go macos windows linux telegram-forum cron scheduler
```

## Documentation

- [Architecture](docs/wiki/Architecture.md)
- [Control Plane](docs/wiki/Control-Plane.md)
- [Quickstart](docs/wiki/Quickstart.md)
- [Telegram UX](docs/wiki/Telegram-UX.md)
- [Security](docs/wiki/Security.md)
- [Operations](docs/wiki/Operations.md)
- [Changelog](CHANGELOG.md)
- [Contract matrix](docs/research/contract-matrix.md)
- [Validation notes](docs/testing/validation-notes.md)
- [ADRs](docs/adr/)

## License

Apache License 2.0. This keeps the project permissive for the community while also providing an explicit patent grant that large companies usually expect from infrastructure and developer-tooling projects.

## Operational Notes

- Telegram long polling returns `409 Conflict` when another process consumes the same bot token.
- Do not expose Codex App Server on a public interface. `codex-tg` is designed around local/private App Server connectivity.
- Keep bot tokens, Telegram sessions, SQLite databases, logs, and `.env` files out of git.
