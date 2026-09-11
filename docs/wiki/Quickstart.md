# Quickstart

## 1. Prepare Telegram

Create a bot with [BotFather](https://t.me/BotFather) and keep the token private. Create one private supergroup, enable Topics, and add only yourself and the bot. Make the bot an administrator with permission to manage topics and delete messages.

Record your positive numeric user id and the negative forum group id, which normally starts with `-100`.

## 2. Check Codex

Install and sign in to the [OpenAI Codex CLI](https://developers.openai.com/codex/cli), then verify App Server availability:

```bash
codex --version
codex app-server --help
```

[Codex App Server](https://developers.openai.com/codex/app-server) is currently experimental. Keep it local and do not expose it directly to the internet.

## 3. Download and build

Install Git and Go `1.26` or newer, then run:

```bash
git clone https://github.com/Buggytheclown/codex-tg-sync.git
cd codex-tg-sync
go build -buildvcs=false -o ctr-go ./cmd/ctr-go
```

This fork is currently distributed from source and does not yet publish GitHub Release binaries.

## 4. Configure and run

On macOS, install and start the user LaunchAgent:

```bash
./ctr-go service install --start --start-at-login
./ctr-go doctor
```

The six-step wizard writes `~/.codex-tg/config.env` and asks for the bot token, user id, forum group id, default work directory, Codex Chats directory, and Codex binary. Keep the cloned directory in place because the service points to the built `ctr-go` binary there.

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

## 5. Enable synchronization

For the first activation, send these commands in the forum's built-in `General` topic. A successful `/sync on` renames it to `Control`:

```text
/sync on
/status
/projects
```

The bot command menu is scoped only to this forum. `/sync on` validates the private two-member forum, materializes recent Codex chats, and discovers later chats. Send text in a task topic to start or steer work; use `/stop` there to interrupt the active turn.

`/projects` creates a topic draft from an existing known workspace. `/newchat` creates a dated Codex Chat. Telegram does not accept arbitrary filesystem paths.

After a daemon restart, Sync is intentionally `off`. Re-enable it in `Control` after App Server reconnects.

## 6. Check requests and pollers

```text
/pollers
/requests
```

`/pollers` shows which optional sources are enabled and whether they are actively succeeding. `/requests` lists active launch requests and their current state.
