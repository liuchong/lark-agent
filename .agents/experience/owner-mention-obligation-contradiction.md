# Owner mention obligation contradiction

For delegated group replies, treat an explicit `@Owner` plus an action
obligation as stronger than the semantic model's suppressive label.

- If the target text, copied obligation quote, or model-provided
  `target_intent` says the owner should fix, check, update status, reply, or
  otherwise act, `no_reply_needed` is an invalid semantic result.
- Do not silently normalize this contradiction to ignored work. Fail the
  semantic resolution so the queue can retry, dead-letter with a useful reason,
  or continue through the normal delegated-reply path.
- Cover both layers: resolver unit tests catch malformed model output, and
  daemon integration tests prove the work item is not completed as ignored.
