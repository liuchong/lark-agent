# Semantic No Reply Obligation

## Problem

Recent private and group delegated-reply work items were dead-lettered with
`owner_reply_ambiguous` even though the target messages were ordinary product
statements, follow-up information, or social acknowledgements. The main answer
model never ran, so terminal finalization did not apply.

A later recurrence showed a second failure mode: the semantic resolver returned
`answered` with valid later owner message IDs, but confidence was below
`policy.owner_reply_confidence_min`. The runtime retried it as ambiguous and
eventually sent the same dead-letter owner summary.

Examples:

- private product/design statement: "目前我不打算区分表情包和 GIF..."
- private follow-up statement: "大概就这样只有一个菜单"
- group owner mention acknowledgement: "@测试负责人 [赞]这图有禅意啊"

## Behavior

- A target enters delegated-reply handling only when it contains a clear
  question, request, invitation, or action obligation for the owner.
- A suppressive semantic result (`answered`, `no_reply_needed`, or `withdrawn`)
  uses a lower safety floor than `unanswered` because it does not send anything
  on the owner's behalf. `answered` still requires validated later owner
  messages or owner acknowledgement reaction.
- Ordinary private product/design statements, answers, acknowledgements,
  continuations, and information sharing without an explicit ask normalize to
  `no_reply_needed`.
- Explicit group `@Owner` remains the group entry condition, but a social
  acknowledgement, compliment, reaction, or information-only statement can still
  normalize to `no_reply_needed`.
- A real action request such as asking the owner to confirm, investigate, look
  into, handle, reply, or send something cannot be silently suppressed by a
  model-provided `no_reply_needed`.

## BDD Acceptance

- Given a private target says not to distinguish sticker packs from GIFs and
  does not ask the owner to do anything, when the semantic model calls it
  `unanswered`, then runtime normalizes it to `no_reply_needed`.
- Given a group target explicitly mentions the owner only to react to or
  compliment an image, when the semantic model returns `ambiguous`, then runtime
  normalizes it to `no_reply_needed`.
- Given a group target explicitly mentions the owner and asks for confirmation,
  when the semantic model returns `no_reply_needed`, then validation rejects the
  result and keeps the target in delegated handling.
- Given a private target is a continuation of an owner-initiated discussion and
  the semantic model returns `answered` with valid later owner message IDs at
  confidence `0.82`, when `policy.owner_reply_confidence_min` is `0.85`, then
  runtime completes the work as owner-handled instead of retrying to dead-letter.
