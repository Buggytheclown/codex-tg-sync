# ADR-032: Sync-only Telegram Surface

Status: Accepted

## Context

The daemon currently contains two Telegram products: a direct-message observer
with bindings, panels, Details, Plan, and settings; and the Sync forum-group
surface used for synchronized Codex chats and external launch requests. The
dual router adds configuration, persistence, lifecycle, and test coupling while
the operator uses only Sync.

## Decision

The configured Sync forum group is the sole Telegram ingress and egress target.
The daemon admits exactly one configured user in that exact group and rejects
all other updates before route lookup or App Server work.

Telegram commands are removed from the default Bot API scope and registered
only for the Sync chat. Startup enforces a delivery allowlist: `health`,
`external_terminal`, and `sync_activation` deliveries addressed to the Sync
group remain active. Supported deliveries interrupted in `processing` are
returned to `retry` during startup.

Sync creates topics only for Codex chats created or updated in the last 24 hours.
It periodically marks topics whose Codex chats have been inactive for 24 hours,
and ready empty drafts of that age, as cleanup-only. Sync stays active when the
initial fresh-chat snapshot is empty, so later chats can be discovered.
Starting, active, and unknown work is never selected. Telegram deletion remains
two-phase, and startup retries cleanup rows from every historical Sync session
for the configured group.

Legacy writer, observer, panel, Details, Plan, settings, binding, and DM routing
code is removed. Fresh databases stop creating their legacy-only tables.
Existing databases keep those tables inert; no destructive migration is run.

Shared Sync helpers are retained under current-mode names. App Server wire-event
compatibility is not part of this retirement.

## Consequences

- Setup requires `CTR_GO_SYNC_GROUP_ID` and exactly one value in
  `CTR_GO_ALLOWED_USER_IDS`.
- Old DM buttons and queued observer deliveries cannot execute after upgrade.
- `/pollers`, `/requests`, and external launch request flows remain independent
  of the retired observer.
- Historical ADRs and release notes remain as history; this ADR supersedes the
  active Telegram observer, panel, Details, Plan, and settings contracts in
  ADR-001 through ADR-016 where they conflict.
