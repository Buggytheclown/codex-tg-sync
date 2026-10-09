# Overview autosave

Save Overview title, description and priority on field blur. Remove the ordinary Save overview button and explicit-save hint; show saving, saved and failure feedback. Cmd/Ctrl+Enter remains an optional flush. Existing Ticket/Codex forms and Polling save/activation controls keep their own behavior.

Do not write unchanged drafts or invalid empty titles. Keep a small task-scoped draft/save queue across tab/card navigation, including blurred edits made while a request is pending. Failed/stale drafts remain available on return; retry is explicit after failure and reload is explicit after a conflict. Never silently overwrite external fields or discard typing.

Autosave must not swallow the destination click through a global busy render. Coordinate other mutations of the same task with pending saves and retain expected-version guards; only owned authoritative replies may advance the editable baseline. Preserve prompts/execution/source settings and existing stale-reply protection. No backend/API/schema/Telegram changes.

Acceptance covers field blur, no-op/validation, multiline text, serialized pending edits, navigation and draft return, failure retry, conflicts, separate attachment actions and unchanged Polling saves. Independent browser QA uses isolated fixture data and the real local API with fake external boundaries; deploy checked static assets without restarting Sync.
