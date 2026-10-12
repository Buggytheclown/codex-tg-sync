# Local task board UI

React, TypeScript and Vite frontend for the daemon's loopback control API.
SQLite is the authority for tasks and source definitions; the UI refetches the existing
read model after commands, SSE invalidations, reconnects, window focus, and a
30-second reconciliation interval.

## Production

Run from `ui/`:

```sh
npm ci
npm test
npm run build
mkdir -p "$HOME/.codex-tg/ui"
cp -R dist/. "$HOME/.codex-tg/ui/"
```

Configure the daemon with `CTR_GO_CONTROL_API_LISTEN=127.0.0.1:8765` and an explicit
`CTR_GO_UI_STATIC_DIR` pointing to the expanded absolute path of
`$HOME/.codex-tg/ui`, then restart it. Set this in the daemon's configuration;
an export in an interactive shell does not configure an existing launchd service.
Install assets in the daemon's application directory because macOS privacy
restrictions can prevent launchd from reading a checkout under Documents while
the API still responds. Do not rely on the service's working directory or on
literal `$HOME` expansion inside a configuration value.
Open [the local board](http://127.0.0.1:8765). A missing build produces an explicit
503 instruction. Unknown `/v1` routes and missing assets never return the SPA.
No machine-specific paths belong in the repository.

For every production UI update, run the build and copy commands again, then
refresh the browser. `npm run build` only changes the checkout's `dist`; the
daemon serves the installed copy. UI asset updates do not require a Go rebuild.

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
  with a `create_task` polling action and its own child task definition.
- A source has one settings page: **When to create a task** holds its trigger,
  schedule, explicit enable/pause action and diagnostics; **Task to create** holds title, prompt
  and working directory, plus model/reasoning and optional repeat-work settings.
  Saving the trigger and child definition uses one version-guarded command.
  Sources create new Inbox tasks and require Telegram launch approval.
- Move cards by dragging them or using the accessible **Move task** dropdown.
  Reading a card never moves it. An invalid move to Polling opens its form.
- **No polling**, **Configured · paused**, and **Waiting** are separate labels.
  Settings are active only while the card is in Polling. Diagnostics show next
  check, last attempt, last success and an error when present.
- Running/completed are independent chat projections. A card without a run has
  neither label; failed and interrupted terminal runs are completed executions.
- Initial and continuation prompts are separate. Source edits affect future
  tasks; task Settings edits affect the selected task. Optional child waiting
  settings start inactive. Generated tasks link back to their source and keep
  independent copied settings. Scheduled repeat work requires an explicit
  positive PR ID; PR event placeholders cannot supply its reviewer or author.
  Pending requests retain
  their frozen prompt and execution settings.
- The UI exposes launch request status and Codex links. Start, dismiss, retry
  and approvals remain in the existing Telegram Control topic.
- `?task=<id>` selects a card without changing it. **Add chat** accepts a thread
  ID or a `codex://threads/<id>` link and is idempotent by thread ID.

`npm test` covers these boundaries and the PR condition validation. The Go
controlapi suite checks Host/Origin/JSON admission, conflicts, SSE invalidation,
and missing static/API assets.

## Appearance

The board uses an Arcanum-inspired dark theme with gray-purple surfaces, blue
links and purple primary actions. Shared color tokens in `src/style.css` cover
the board, inspector, forms, diagnostics and workflow states. YS Text loads from
`yastatic.net`; if the font CDN is unavailable, the system sans-serif fallback
keeps the board usable. Native controls also follow the dark color scheme.
