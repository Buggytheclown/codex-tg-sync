# ADR-038: Manual actions before maintenance

Status: implemented, verification recorded in the process validation report.
Amends ADR-033 and ADR-037.

Foreground includes new launch cards, newly observed execution outcomes and the
results of explicit Telegram recovery. Repainting an already shown execution
status is background. Startup dirties fingerprints without erasing rendered
status, including failed/unknown cards. SQL selects foreground first; a blocked
background render cannot retain the only route to the transport's foreground
queue. The existing render loop has one bounded background worker and per-request
in-flight ownership. Same-request promotion cancels a parked maintenance edit.

The existing governor retains its pacing, background budget, fairness, critical
callback lane and shared cooldown. No new scheduler, queue or transport is added.
The existing request journal stores manual and rendered priority versions; a
render can acknowledge only its observed version. Manual known-topic delivery
retains the request ID on the topic's existing delivery revision until completion,
so an intermediate card cannot downgrade the final result. Manual provenance
also travels in the existing topic-creation payload; creation outcome, startup
recovery and binding materialization promote the result in their own transactions. New columns are
additive and old cards acquire no fabricated manual intent.

Background App Server reads, discovery and subscription cleanup release syncMu
around remote waits. A result is applied only to the same Sync session, transport
source/generation, topic route, writer reservation and turn observation. Reconnect
also invalidates a cached reader for subsequent targets. Subscription operations
are exclusive per connection/thread; pending old writer cleanup orders a same-
target resume without retaining the global lifecycle or task admission gate.
Unrelated manual launches continue while it waits. Resume/unsubscribe are never
replayed merely because their response became stale.

The exact-target safety ordering, authority checks, user/group admission,
uncertain dispatch fences and terminal-delivery identities remain in force.
