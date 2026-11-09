# Manual actions before background maintenance

## Problem and objective

A new launch card waited behind historical card repaints despite the existing
foreground/background Telegram governor. Accepted dispatch also takes the same
lifecycle mutex as background App Server reads. The objective is to apply the
existing priorities before transport and let manual work proceed during unrelated
background IO.

## Required behavior

1. New launch cards, newly changed execution outcomes, and explicit Telegram
   recovery results are foreground work. Repainting an already shown execution
   outcome after startup, projection invalidation or periodic maintenance is
   background work. Classification survives restart; execution status is not
   erased to manufacture priority.
2. Pending foreground cards are selected before historical repaints, including
   when more than a full selection batch of old cards exists. A background card
   waiting on transport cannot hold the only renderer needed by another card.
   Each request retains one in-flight render owner; when a request changes during
   an old render, its latest state remains due and is reconciled.
3. Existing foreground/background pacing, the background budget, bounded fairness,
   shared retry_after and critical callback acknowledgement remain authoritative.
   Already executing network calls and a shared Telegram cooldown cannot be
   promised instantaneous preemption. There is no new generic scheduler or queue.
4. Start, input, Stop and approval handling can acquire lifecycle ownership while
   periodic/event-driven reads, discovery or subscription maintenance await IO.
   Required authority checks for the requested target still apply. Mutations on
   that exact connection/thread retain ordering; awaiting that prerequisite is
   cancellable and does not hold other targets behind the lifecycle gate. Background IO
   does not retain the lifecycle mutex while awaiting remote responses.
5. A response returning after Sync off/on, poll reconnect, topic replacement or
   another writer/turn transition cannot overwrite the new lifecycle, release
   its owner or publish a stale terminal outcome. Stale observations are discarded
   and reconciled later without replaying a mutating RPC.
6. Card fingerprints, delivery identities, route/user/group admission, legacy
   history and separate Codex/Telegram recovery stay intact. Maintenance must not
   enqueue historical terminal notices, resume passive old chats or start work.
7. Validation covers public behavior with blocked IO, foreground selection beyond
   a full historical batch, genuine terminal notification once, reconnect and
   ownership changes during an outstanding read, transport classification and
   existing governor limits. Live QA uses isolated requests and authenticated
   Telegram readback, without a second getUpdates consumer or replaying real work.

## Scope and authorization

The user explicitly authorizes specification checking, implementation,
verification, commit and installation. Reuse the shared App Server, existing
SQLite request journal and Telegram governor. Preserve unrelated working changes.
No force push, PR publication, source poller redesign or task-board UI changes.
Deployment preserves Sync and confirmed Codex work. Before restart inspect pending
accepted dispatch and let its sending boundary settle; do not repeat the incident
where an update interrupted an accepted Run.

## Evidence and acceptance boundaries

The incident's card was delivered about 155 seconds after creation while old
repaints consumed foreground slots. A separate controlled test proves an unrelated
blocked board read pins dispatch at starting/not_sent until released. Historical
logs do not prove that mutex caused the incident's click-to-restart interval.
No arbitrary latency SLA is introduced: tests require the foreground operation to
reach its external boundary before the blocked background operation is released.
