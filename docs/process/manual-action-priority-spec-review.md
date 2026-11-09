# Specification check: manual-action priority

Ready for implementation with the clarifications now included in the specification.

- Confirmed defect: changing only Telegram flags would leave a foreground card
  behind the single renderer blocked in background IO. Requirement 2 checks that
  case explicitly and requires per-request ownership.
- Confirmed defect: merely unlocking background reads permits stale observations
  to overwrite a newly started turn or an off/on session. Requirement 5 requires
  validating session, poll generation, routing and writer/turn ownership before
  applying results; a fast successful dispatch alone does not satisfy acceptance.
- Consequential ambiguity resolved: manual Telegram recovery can change only the
  projection of a terminal request. Requirement 1 includes that intent without
  resetting the already-rendered execution status or resending old terminal notices.
- Limits clarified: foreground remains subject to the shared Telegram cooldown
  and bounded fairness. In-flight calls cannot be recalled; queues and lifecycle
  locks must not add an unrelated wait. Existing numerical budgets are retained.
- Delivery condition: check accepted unsent work before restarting, and preserve
  confirmed turns and Sync. This is necessary given the interrupted Run incident.

Sources checked: ADR-033, ADR-037, external request SQL selection/rendering,
Telegram groupWrite/governor, shared mutex dispatch and reconciliation paths,
subscription maintenance, and the isolated blocked-read diagnostic.
No remaining user decision is required for this scope.
