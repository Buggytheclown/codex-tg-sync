# Sync Long Final Delivery

## Problem

Sync currently sends the complete Final as one Bot API `sendMessage` request.
Telegram limits message text to 4096 UTF-16 code units, so a longer Codex Final
is rejected. The status message remains visible, the Final fingerprint remains
pending, and reconciliation keeps retrying the same invalid request without an
operator-visible diagnostic.

## Decision

- Split only Sync Final messages. Status messages keep their existing single
  editable-message contract.
- Reuse the shared `tgformat` UTF-16-aware splitter with the Telegram message
  limit instead of adding another length implementation.
- Put `✅ [Final]` on every chunk. Telegram previews the latest topic message,
  so a headerless continuation hides the completed state in the topic list.
- Send chunks in order and update `last_final_fp` only after every chunk is
  accepted by the Bot API.
- Log a sanitized delivery warning with thread, turn, topic, and chunk
  coordinates when any chunk fails. Reconciliation remains the retry owner.

## Alternatives

- Truncation was rejected because it silently loses part of the Codex answer.
- Document-only delivery was rejected because it makes normal Finals harder to
  read in Telegram.
- Durable per-chunk delivery state was deferred. It would prevent a duplicate
  prefix after a rare partial multi-chunk failure, but requires schema and
  recovery complexity beyond this MVP fix.

## Validation

- A daemon test proves a Final over 4096 UTF-16 units becomes multiple ordered
  Telegram messages, every chunk stays within the limit and keeps the Final
  header, and the fingerprint is committed only after all chunks succeed.
- A failure test proves a failed continuation leaves the fingerprint pending.
- Targeted tests, `go test ./...`, and `go build -buildvcs=false ./...` pass.
- Live validation reads the affected Telegram topic after daemon restart and
  confirms the previously pending long Final appears in chunks.
