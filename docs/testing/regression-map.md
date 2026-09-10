# Regression Map

Use this map for changes to the active Sync Telegram surface. ADR-032 supersedes legacy direct-message observer contracts in older ADRs.

## Admission, setup, and commands

Primary tests:

- `internal/daemon/retirement_test.go`: out-of-group messages and callbacks are rejected before storage; retired settings are ignored.
- `internal/config/config_test.go`: Sync group and exactly one allowed user, transport modes, paths, and external adapters.
- `cmd/ctr-go/main_test.go`, `cmd/ctr-go/service_test.go`: init/service setup writes the Sync-only configuration.
- `internal/telegram/api_test.go::TestClientScopesCommandsToExactSyncChat`: exact Bot API scope payload.
- `internal/telegram/bot_test.go::TestBotStartScopesCommandsToConfiguredSyncGroup`: startup clears default commands before setting the Sync chat menu.
- `internal/telegram/bot_test.go::TestDefaultCommandsExposeSyncAndOperatorStatusCommands`: exact eight-command menu.

Required invariants:

- Reject before SQLite or App Server.
- No direct-message compatibility router.
- Startup fails closed if command scoping fails.
- `/repair` remains hidden but accepted in Control.

## Sync activation, discovery, and transport

ADRs: ADR-020 through ADR-027, ADR-029, ADR-032, and ADR-033.

Primary suites:

- `internal/daemon/sync_test.go`
- `internal/storage/store_sync_test.go`
- `internal/appserver/client_test.go`
- `internal/appserver/writer_manager_test.go`
- `internal/telegram/api_test.go`

Coverage includes:

- forum validation and Control preparation;
- paginated, importance-first `/sync on` topic selection (including `notLoaded` threads with running or terminal last turns), initial topic limit, and continuous discovery;
- one topic per thread and receipt idempotency;
- shared daemon/WebSocket reconnection without prompt replay;
- startup reset to off and cleanup-only old topics/drafts;
- generation-aware writer claims and fail-closed unknown dispatch;
- poll/list/read behavior and non-blocking startup;
- typed Telegram topic/retry failures;
- one-second group-write pacing with no more than 20 raw attempts per rolling 60.25 seconds, bounded foreground preference, and shared `retry_after` cooldown.

## Prompt, steer, stop, and lifecycle

Primary suite: `internal/daemon/sync_test.go`.

Required scenarios:

- first topic prompt resumes/starts exactly one turn;
- duplicate message ids do not replay;
- active topic text steers the current turn;
- stale-active rejection re-reads authority before replacement start;
- authoritative newer turns release stale local ownership;
- Stop checks current topic/thread/turn/generation;
- terminal events and terminal poll evidence close writer leases;
- active status timers and open-block durations advance in ten-second buckets,
  while content/state changes can still edit immediately and terminal duration
  stays exact and stable;
- one status message is created per turn and then edited in place across title
  changes, mirrored user messages, and direct-delivery refreshes;
- terminal status is delivered before Final in the same per-topic sequence, so
  no queued status update can move behind that turn's Final;
- restart/reconnect does not duplicate user receipts or finals;
- long finals preserve UTF-16 limits and number every Final chunk (`1/N` through `N/N`).

Terminal ambiguity logic lives in `internal/daemon/terminal_gate_test.go`. Preserve defer windows, explicit interrupts, and authoritative terminal evidence.

## Approvals and structured input

Primary tests:

- `internal/daemon/sync_test.go::TestSyncApprovalCallbackIsGuardedByTopicTurnAndGeneration`
- `internal/daemon/sync_test.go::TestSyncApprovalDecisionsResolveSameCard`
- structured input callback tests in `internal/daemon/sync_test.go`

Required invariants:

- only daemon-owned requests are actionable;
- callback coordinates and writer generation are checked before App Server response;
- `acceptForSession` is labeled `Allow command prefix`;
- accept, acceptForSession, decline, and cancel edit the original card to a terminal status;
- the edit path has no inline keyboard;
- duplicate/stale callbacks fail closed.

App Server request normalization remains covered in `internal/appserver/*_test.go`; removing a Telegram presentation must not remove wire compatibility.

## Storage and upgrade safety

Primary tests:

- `internal/storage/store_test.go::TestLegacyTelegramTablesAreNotCreatedOrDropped`
- delivery retirement matrix in `internal/storage/store_test.go`
- Sync state tests in `internal/storage/store_sync_test.go`
- callback, receipt, request, delivery, and snapshot repository tests across `internal/storage/*_test.go`

Required invariants:

- fresh DB has no retired Telegram tables;
- old DB tables are left untouched;
- startup supersedes pending/retry/processing deliveries outside the Sync group and retired observer delivery kinds;
- historical delivered/dead rows remain historical;
- current health, external terminal, and Sync activation deliveries to the Sync group remain eligible;
- startup returns supported in-group deliveries interrupted in `processing` to `retry`;
- `/sync on` stops after the first ambiguous topic-creation outcome, marks later candidates skipped, and queues its Control summary durably with the activation state.

## Launch requests

ADRs: ADR-028 through ADR-031.

Primary suites:

- `internal/daemon/external_requests_test.go`
- `internal/storage/store_external_requests_test.go`
- `internal/cronpoller/*_test.go`
- `internal/ymessenger/*_test.go`
- `internal/arcanumreview/*_test.go`

Required scenarios:

- ingestion and cursor advancement are atomic/idempotent;
- a `[Launch request]` card exists before manual or automatic start;
- manual Start/Dismiss and Retry/Close edit the same durable card;
- callback acknowledgement does not wait for launch-card Telegram I/O;
- only admission-valid callbacks are pre-acknowledged, before business handling and regardless of group cooldown;
- `createForumTopic` `429` performs one launch-owned attempt and leaves a failed card with Retry/Close;
- auto-start author policy applies only to new Arcanum requests;
- start claims once and ambiguous thread creation is not replayed;
- terminal state closes the request and external reply delivery is retryable/idempotent;
- `/requests` enters through public Sync Control routing and lists active requests;
- unauthorized YMessenger messages cannot create or start Codex work.

## Poller and health status

Primary tests:

- poller status tests in `internal/daemon/external_requests_test.go`
- health tests in `internal/daemon/health_test.go`
- source-specific poller tests under `internal/cronpoller`, `internal/ymessenger`, and `internal/arcanumreview`

Required invariants:

- `/pollers` enters through public Sync Control routing;
- output distinguishes disabled, polling, healthy, degraded, and failed sources;
- last attempt/success and consecutive failures are truthful;
- sleep/resume gaps do not cause false flapping;
- warning/recovery order is preserved;
- health deliveries fall back to General only inside the configured Sync group.

## Configuration and secrets

Primary tests:

- `internal/config/config_test.go`
- `cmd/ctr-go/main_test.go`
- `cmd/ctr-go/service_test.go`
- secret-redaction tests in daemon, config, storage, and Telegram packages

Configuration must not reintroduce retired chat allowlists, observer intervals, notification switches, panel modes, or Telegram model/settings state. External tokens and Telegram Bot API URLs must remain redacted in config JSON, logs, SQLite errors, and snapshots.

## Required checks

Run targeted tests first, then:

```powershell
go test ./...
go test -race ./internal/daemon ./internal/storage ./internal/telegram
go build -buildvcs=false ./...
git diff --check
```

For live Telegram QA, verify:

1. command menu exists only in the Sync group;
2. a direct message produces no response or state change;
3. `/status`, `/pollers`, and `/requests` work in Control;
4. `/sync on` creates/updates topics;
   running/waiting chats are created before unknown, failed/interrupted, and completed chats;
5. a topic prompt starts with explicit writable/on-request permissions;
6. an available approval edits the same card and removes buttons;
7. restart resets Sync off without interrupting the authoritative Codex turn.
8. during a Telegram `429`, callbacks are acknowledged while noncritical group writes wait for `retry_after`.
