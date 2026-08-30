# Validation Notes

This document records the current validation protocol. Historical direct-message observer validation was removed with ADR-032; Git history retains the old evidence.

## Automated checks

```powershell
go test ./...
go test -race ./internal/daemon ./internal/storage ./internal/telegram
go build -buildvcs=false ./...
git diff --check
```

The regression map names the focused suites for admission, setup, Sync lifecycle, writer ownership, approvals/input, storage upgrades, requests, pollers, health, and delivery.

## Live Telegram checks

When a configured contour is available:

1. Inspect Bot API command scopes: default is empty and the exact Sync group has the eight public commands.
2. Send a direct message and verify there is no response, route, callback, or App Server work.
3. In Control, verify `/status`, `/pollers`, `/requests`, and `/projects`.
4. Enable `/sync on`; verify topic creation/reconciliation and no duplicate topics.
5. Send topic text; verify the correct thread starts or steers with explicit `on-request`, `auto_review`, and `workspace-write`.
6. Verify `/stop` interrupts the current authoritative turn.
7. When available, resolve each approval/input action and verify the original card is edited without buttons.
8. Exercise a launch request: visible card before start, active state, terminal state, and source reply when supported.
9. Restart and verify Sync resets off while shared App Server work remains authoritative.

Record only public-safe conclusions. Never record real chat ids, user ids, thread ids, tokens, local paths, logs, databases, or screenshots containing private data.
