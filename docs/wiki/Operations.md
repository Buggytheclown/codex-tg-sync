# Operations

## Status

```powershell
ctr-go status
ctr-go service status
```

In Control:

```text
/status
/pollers
/requests
```

`/pollers` shows enabled state, current activity, last attempt/success, and consecutive failures. `/requests` shows active non-closed launch requests.

## Repair

```powershell
ctr-go repair
```

or in Control:

```text
/repair
```

Repair recreates the bridge poll session and refreshes synchronization state. It does not restart `codex-tg`, start a missing managed App Server, delete Codex threads, or re-enable Sync.

## Cron requests

Create `~/.codex-tg/cron.json` for daily or weekly requests. The daemon reloads it every 30 seconds. Use `launch_policy: telegram` for Start/Dismiss approval or `auto` for automatic start. Invalid configurations do not create requests and appear in logs/poller status.

## Arcanum review requests

Configure the authenticated `ya` binary, reviewer login, working directory, poll interval, and Requests topic:

```text
CTR_GO_ARCANUM_REVIEW_ENABLED=true
CTR_GO_ARCANUM_REVIEW_LOGIN=<reviewer-login>
CTR_GO_ARCANUM_YA_BIN=/absolute/path/to/ya
CTR_GO_ARCANUM_REVIEW_POLL_SECONDS=60
CTR_GO_ARCANUM_REVIEW_CWD=/absolute/path/to/projects
CTR_GO_EXTERNAL_REQUESTS_TOPIC_ID=<permanent-requests-topic-id>
```

Optional `CTR_GO_ARCANUM_REVIEW_AUTO_START_AUTHORS` is a comma-separated exact author allowlist. Matching new PRs get a visible request card and then auto-start; all others require Start.

## macOS service

```powershell
ctr-go service install --start --start-at-login
ctr-go service status
ctr-go service stop
ctr-go service start
ctr-go service restart
ctr-go service disable-login
ctr-go service enable-login
ctr-go service uninstall --keep-config
```

The LaunchAgent receives `CTR_GO_CONFIG`; secrets and required proxy values remain in the private config file.

## Restart behavior

In shared App Server modes, restarting `codex-tg` does not interrupt the authoritative Codex turn. It resets Sync to off and cleans the previous Telegram session. Run `/sync on` after reconnect.

In spawned mode, the bridge owns the App Server process. Avoid restarting while a bridge-owned turn is active.

A Telegram `409 Conflict` means another process is polling the bot token.
