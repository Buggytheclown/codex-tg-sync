# Shared board visual design

Unify the local board around compact cards and a readable task brief in Overview. Use a shared light neutral palette with blue navigation/selection, consistent typography, surfaces, borders, spacing and focus states. Polling/launch warnings remain distinct from model activity; Completed never implies success or acceptance.

Cards become one visually bounded object with type/drag affordance, one editable priority control, title, two-line multiline description, independent polling/execution indicators, Telegram request notice and integrated ticket/Codex footer. Empty connections remain actionable. Controls stay outside the draggable selection button; wrapping and full-wrapper insertion geometry remain intact.

Overview keeps editable native title/description and priority, existing chat/ticket attachment/editing, provenance/source links, state/move controls and Save overview with its keyboard/version/draft guards. Remove polling diagnostics/conditions, metadata dates and launch requests/frozen prompt from Overview. Their stored data and the existing Polling/History tabs remain unchanged. A readable inline appearance does not add a new save model or contentEditable editor.

Apply the same visual system to the header/search, source sidebar, columns/empty states, detail tabs, numbered Polling/SourceEditor workflow, History, creation/attachment forms and loading/error/conflict/saving states. Preserve future-child purple and merge-terminal green semantics. Do not copy decorative invented fields from mockups, add dependencies, or change database/API/Telegram/launch behavior.

Acceptance: native control accessibility/keyboard use, hidden Overview sections, one card priority control, multiline text, independent badges, non-nested links, empty attachment/edit forms, stale draft protection, card selection/moves/order/placeholder, collapsible sources, Done preference and all task/source PR/cron workflow fields. Fresh review and independent browser regressions use isolated fixture data. Deploy the static build without daemon restart or changing Sync.
