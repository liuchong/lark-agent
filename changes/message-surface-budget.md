# Message Surface Budget

## Decision

- GitHub reference markers emitted in Lark messages use a compact v2 payload with
  short JSON keys and a truncated HMAC. The parser continues to accept v1 markers
  already present in historical messages.
- Smart-command Lark text is capped after the reference marker is appended. If
  the model fills the whole message budget, the runtime truncates model text and
  preserves the marker so follow-up replies can still resolve the GitHub event.
- No rolling time-window deduplication is added in this change. Existing stable
  idempotency keys already dedupe the same GitHub event/action send. Suppressing
  different events within a time window would risk hiding distinct CI failures or
  follow-up facts, so it needs a separate product rule before implementation.

## Non-goals

- This does not disable Lark link previews. That is not exposed by the send
  message API.
- This does not change GitHub check, comment, or release output limits.
