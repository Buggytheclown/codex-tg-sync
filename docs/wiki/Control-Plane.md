# Control Plane

The control plane separates Codex identity and lifecycle from Telegram transport details.

## Responsibilities

- list/read/start/resume Codex threads;
- start/steer/interrupt turns;
- answer approval and input requests;
- normalize live and polled App Server state;
- own generation-safe writers;
- persist launch requests, source observations, and delivery attempts;
- expose local status through CLI, tray, Telegram Control, and the optional loopback API.

App Server remains the only authority for interactive thread and turn state. SDK/MCP integrations may orchestrate work but do not replace App Server snapshots or approvals.

## Channel boundaries

Telegram is one AFC forum adapter, not the control core. Telegram ids remain adapter state and never replace `threadId`.

Cron, Yandex Messenger, and Arcanum are ingestion/reply adapters. They create the same durable launch request and use the same guarded dispatch path.

The optional HTTP control API is disabled unless `CTR_GO_CONTROL_API_LISTEN` is configured and accepts loopback TCP addresses only.
