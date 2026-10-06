---
name: codex-task
description: Add the current Codex chat or a supplied Codex chat link to the personal codex-tg board when explicitly asked, returning the existing task on duplicate addition.
---

# Add a chat to the personal queue

Use the local codex-tg HTTP API. This operation pins a chat as a TODO task; it does not launch a model, enable polling, or approve a Telegram request.

Resolve the actual thread ID from the supplied Codex link or current chat context. For the current chat, use an available trusted thread identifier (for example `CODEX_THREAD_ID`) or thread tools. Never infer identity from the chat title, filesystem name, or most recent chat. If the ID cannot be determined, ask for the chat link.

Find the local API address from `CTR_GO_CONTROL_API_LISTEN` or the user's codex-tg configuration. The UI and API share this loopback address. Do not print the full configuration, which may contain credentials. If the service is unavailable, report that directly; do not launch another daemon or edit its SQLite file.

Send an Origin-less HTTP request with `Content-Type: application/json`:

```http
POST /v1/tasks/add-chat
{"thread_id":"<actual thread ID>","title":"<short meaningful title>"}
```

Use a structured HTTP client or a JSON body file rather than interpolating user text into shell code. Only connect to loopback. The API validates the chat and returns its existing task on repeated addition, so a request with uncertain network outcome may be checked by listing tasks and matching the exact thread ID. Do not create another task with a different identity.

Read the response before reporting success. Return the task title and a browser link to the board with `?task=<id>`. A new task starts in TODO; an existing task keeps its current column and settings. Continue the original conversation normally.
