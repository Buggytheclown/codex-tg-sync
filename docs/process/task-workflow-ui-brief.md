# Task workflow inspector

Replace the polling settings inspector with a compact, numbered workflow while retaining the source sidebar, kanban, Overview and History. Product labels are English.

Sources show When, Create task, and optional Waiting settings for new tasks. The last step is visually distinct and explicitly affects future copies only. Source trigger and owned child definition still save together. Source launch policy, cron settings and execution settings remain available.

Ordinary tasks show Wait, After event, Continuation prompt and Approval, with PR merged → Done as a separate terminal option. The monitored PR ID has a direct link; provenance Source URL is separate. Return to Inbox does not request a model. Request continuation needs a linked chat and a nonempty prompt, and still requires Telegram Start in that same chat. Missing readiness is visible before activation. No browser launch or approval bypass is introduced.

One version-checked save persists ordinary task prompts and waiting configuration atomically through an optional polling field on the existing task PATCH. Activation remains a separate guarded move after saving. Omitted polling preserves the existing configuration. Initial prompt, provenance, execution settings and manual tasks without waiting remain editable. Existing frozen launch requests are unchanged.

Acceptance covers atomic resulting-state validation and rollback; stale edits/draft retention; PR links versus placeholders; source and child independence; missing-chat readiness and action selection; keyboard saving; ordinary tasks without polling; PR and cron fields; and existing board controls/order/history. Fresh review and independent regression verification precede installation and commit.
