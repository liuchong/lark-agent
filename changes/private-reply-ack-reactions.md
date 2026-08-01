# Private Reply Direction And Ack Reactions

## Problem

Private messages that are answers to the owner can be mistaken for new coding
handoffs. When the semantic check later retries and fails, the work stops as a
dead letter even though no agent investigation was needed. Owner emoji reactions
on the exact target are also not read, so an explicit owner acknowledgement such
as `Get` is ignored.

## Behavior

- Group-chat delegation still requires a native `@Owner` mention. This change
  does not loosen the group gate.
- In private chats, a target that answers, confirms, or continues an
  owner-led conversation defaults to `no_reply_needed` unless the target text
  contains a new question, request, invitation, or coordination obligation.
- A private `unanswered` result must include `target_intent` and an exact
  `response_obligation_quote` from the target text. Without a valid quote, the
  runtime normalizes answer/acknowledgement/continuation targets to
  `no_reply_needed`.
- Owner reactions `Get`, `OK`, `DONE`, `THUMBSUP`, `CheckMark`, `Yes`, and
  `LGTM` on the exact target count as deterministic owner-handled evidence.
  Other users, bot reactions, unsupported emoji, and reactions on other
  messages do not count.
- Reaction read failures fail closed with bounded semantic retry and are
  recorded as reaction-read failures, not as missing quoted context.

## BDD Acceptance

- Given a private reply equivalent to work `#5517`, when semantic resolution
  runs, then it returns `no_reply_needed` and does not create an investigation,
  approval, or sender reply.
- Given that private reply includes `你帮我查一下代码`, when semantic resolution
  runs, then the quote is accepted as the target response obligation and
  `unanswered/coding` can enter the delegated workflow.
- Given the configured owner reacts to the exact target with `Get`, when any
  semantic owner-handled checkpoint runs, then the target is treated as
  `answered` with reaction evidence and no sender reply is sent.
- Given reaction reading lacks permission, fails, or exceeds the page bound,
  when semantic resolution runs, then the work retries rather than sending.

## Non-goals

- Do not auto-replay or message the historical `#5517` item.
- Do not treat arbitrary emoji, other-user reactions, or bot `Typing` as owner
  acknowledgement.
- Do not persist reaction state as authoritative cache or subscribe to reaction
  events in this change.
