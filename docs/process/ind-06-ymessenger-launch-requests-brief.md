# IND-06 YMessenger Launch Requests Brief

## Problem

An operator can assign work by mentioning a configured robot in Yandex
Messenger. Today that message cannot enter the existing `codex-tg` approval and
Codex thread lifecycle without another daemon, another Telegram update
consumer, or source-specific logic inside Telegram handlers.

## Goal

Add one optional Yandex Messenger adapter to the existing `codex-tg` daemon:

- poll Bot API updates with a durable cursor;
- accept a non-empty robot mention only from a configured sender login;
- create one durable launch request per source message;
- show that request in one configured permanent Telegram forum topic;
- start or dismiss it through inline buttons;
- create a normal Codex thread and first turn through the existing App Server
  and AFC lifecycle after approval;
- keep the normal AFC topic as the surface for all later Codex activity.

The existing `ctr-go` start path remains the only process lifecycle the
operator manages.

## Non-goals

- A generic scheduler, cron engine, plugin system, or polling-script protocol.
- A second daemon, local write HTTP API, shared SQLite access, or second
  Telegram `getUpdates` consumer.
- Filtering source messages by Yandex Messenger chat id.
- Automatic retry after an ambiguous Codex dispatch.
- Automatic creation, rename, deletion, or repair of the permanent approval
  topic.
- Multiple Telegram approval topics or multiple operators.

## UX

The configured approval topic receives one message per launch request:

```text
Yandex Messenger request
From: <sender>

<source message text>

[Start] [Dismiss]
```

The same message is edited as the request moves through `Starting`, `Started`,
`Dismissed`, `Failed`, or `Outcome unknown`. Successful dispatch includes the
durable Codex thread id. A normal AFC topic is created through existing AFC
materialization and carries the live Codex session afterward.

## Architecture

```text
internal/ymessenger
        |
        v
LaunchRequestSink -> SQLite external_launch_requests
        |                         |
        |                         v
        +----------------> Telegram approval renderer
                                   |
                                   v
                         App Server / AFC dispatch
```

- `internal/ymessenger` owns Bot API transport, update parsing, filtering,
  polling, and its source cursor.
- The launch-request domain owns durable state, idempotency, Telegram delivery
  metadata, and dispatch outcome.
- Telegram owns callback delivery and message rendering, but does not interpret
  Yandex Messenger updates.
- App Server remains authoritative for thread and turn state.

## Source Filtering

An update is actionable only when:

- it has non-empty plain text;
- `sender.login` is in the configured allowlist;
- the configured robot login is present in `mentioned_users`;
- it is not a bot/service/self message.

Chat id is retained as source metadata and dedupe identity but is not an
authorization filter.

## Durability

- Yandex Messenger identity is `(source, external_id)`, where `external_id`
  contains source chat and message identity.
- A batch transaction inserts normalized requests and advances the maximum
  processed update id together.
- Replayed updates are harmless because source identity is unique.
- `Start` is a conditional state transition and can claim a request once.
- A possible-but-unconfirmed App Server mutation becomes `outcome_unknown` and
  is never replayed automatically.
- Telegram send/edit failures do not roll back request or Codex state; delivery
  is reconciled from SQLite.

## Configuration

All settings use the existing private config file and normal environment
override precedence. The adapter is disabled by default. Enabling it requires:

- robot login;
- OAuthTeam token;
- one or more allowed sender logins;
- poll interval;
- permanent approval topic id;
- default Codex cwd.

Secret values must be redacted from config JSON, status output, diagnostics,
and errors.

## Acceptance

- [ ] Disabled configuration leaves existing startup and Telegram behavior
      unchanged.
- [ ] One allowed mention from any source chat creates one durable request.
- [ ] A denied sender or missing mention creates no request while the cursor
      still advances.
- [ ] Restart resumes from the persisted update id without losing or duplicating
      a request.
- [ ] One request creates one approval message in the configured topic.
- [ ] Stale or mismatched callbacks fail closed.
- [ ] Repeated Start creates at most one thread and first turn.
- [ ] Dismiss never invokes App Server.
- [ ] Successful Start yields one normal AFC topic through existing sync.
- [ ] The permanent approval topic is never registered for AFC cleanup.
- [ ] Ambiguous dispatch is visible and non-replayable.
- [ ] Unit/integration tests pass; live Telegram/YMessenger validation is
      recorded when credentials and a live contour are available.
