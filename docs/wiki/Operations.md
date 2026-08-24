# Operations

## Status

```powershell
ctr-go status
ctr-go service status
```

In Telegram:

```text
/status
/context
```

## Repair

```powershell
ctr-go repair
```

In Telegram:

```text
/repair
```

Repair is non-destructive: it recreates the bridge's App Server poll session,
rechecks pending approvals, and refreshes tracked thread snapshots. It does not
restart `codex-tg`, start a missing managed App Server daemon, or change AFC
on/off state.

## macOS Service

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

The service is a user LaunchAgent. Its environment contains only
`CTR_GO_CONFIG`; tokens and user ids stay in the local config file.
Proxy variables needed for Telegram/Codex network access are also preserved in
the private config and applied by `ctr-go` after startup, instead of being
written directly into the LaunchAgent plist.

## Restart Safety

In spawned mode, avoid forced bridge restarts while a Telegram-originated run
is active. The bridge owns that App Server stdio session, so killing the bridge
closes the transport and can make the turn appear `interrupted`.

In shared-daemon AFC mode, Codex Desktop and `codex-tg` use the same managed App
Server. Restarting only `codex-tg` does not interrupt the authoritative Codex
turn, but it intentionally resets AFC to `off` and cleans the previous Telegram
topics. Run `/sync on` after the bridge reconnects.

Until a safe restart command exists, prefer this order:

1. Check the active run state in Telegram or with `go run ./cmd/ctr-go status`.
2. Wait for the active Telegram-originated turn to finish.
3. Rebuild or reinstall the binary.
4. Restart the daemon with `ctr-go service restart` or the tray menu.

Future restart work should implement a drain/guard path that refuses or delays restart while Telegram-origin turns are active. More invasive designs, such as a separate App Server broker process or reattaching to a turn after daemon death, require a separate ADR.

## Common Issue

Telegram `409 Conflict` means another process is polling the same bot token. Stop the other consumer before starting `codex-tg`.

## Local Config

`ctr-go init` and `ctr-go service install` write `~/.codex-tg/config.env` by
default. Set `CTR_GO_CONFIG` to use another path. Environment variables override
values from the config file, which lets LaunchAgent/systemd/manual deployments
keep their existing overrides.
