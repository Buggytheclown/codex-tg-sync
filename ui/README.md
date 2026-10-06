# Local task board UI

React, TypeScript and Vite frontend for the daemon's loopback control API.
SQLite is the authority for tasks and templates; the UI refetches the existing
read model after commands, SSE invalidations, reconnects, window focus, and a
30-second reconciliation interval.

## Production

```sh
npm ci
npm test
npm run build
```

Configure the daemon with `CTR_GO_CONTROL_API_LISTEN=127.0.0.1:8765` and an explicit
`CTR_GO_UI_STATIC_DIR` pointing to this directory's `dist` subdirectory.
Open [the local board](http://127.0.0.1:8765). A missing build produces an explicit
503 instruction. Unknown `/v1` routes and missing assets never return the SPA.
No machine-specific paths belong in the repository.

## Development

```sh
npm ci
npm run dev
```

The dev server listens on `127.0.0.1:5173` and proxies API requests to
`http://127.0.0.1:8765`. Set `VITE_API_TARGET` to a different loopback API origin
when needed. Configure `CTR_GO_UI_DEV_ORIGIN=http://127.0.0.1:5173` explicitly
for development: the proxy changes Host while retaining the browser's Origin.
Open the dev server using that exact origin. No wildcard CORS is used.

## Operator behavior

- Sources stay in the left list, including paused sources. A source is a task
  with a `create_task` polling action and a selected child template.
- Move cards by dragging them or using the accessible **Move task** dropdown.
  Reading a card never moves it. An invalid move to Polling opens its form.
- **No polling**, **Configured · paused**, and **Waiting** are separate labels.
  Settings are active only while the card is in Polling. Diagnostics show next
  check, last attempt, last success and an error when present.
- Running/completed are independent chat projections. A card without a run has
  neither label; failed and interrupted terminal runs are completed executions.
- Initial and continuation prompts are separate. Template edits affect future
  tasks; Settings overrides affect the selected task. Pending requests retain
  their frozen prompt and execution settings.
- The UI exposes launch request status and Codex links. Start, dismiss, retry
  and approvals remain in the existing Telegram Control topic.
- `?task=<id>` selects a card without changing it. **Add chat** accepts a thread
  ID or a `codex://threads/<id>` link and is idempotent by thread ID.

`npm test` covers these boundaries and the PR condition validation. The Go
controlapi suite checks Host/Origin/JSON admission, conflicts, SSE invalidation,
and missing static/API assets.
