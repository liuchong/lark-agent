# Approval Preserves Reply Identity

An exact reply approval must persist both the draft content and the invocation
relevance that selects its Lark sender identity. Text, reason, and owner action
alone are insufficient: an assistant request and a delegated owner mention can
otherwise resume through the same approval path with different required
senders.

Include relevance in the approval request JSON and idempotency key. Restore it
before the reply controller selects `ReplyAsBot` or `ReplyAsUser`. Keep the
legacy empty-relevance path only for already persisted approvals: recover its
identity from the work item's durable decision, consume the legacy exact-draft
key, and fail before sending if that decision is absent or invalid. Cover both
new and legacy durable resume paths with integration tests rather than testing
the reply controller in isolation.

Approval state and approval policy are different facts. Once an exact delegated
draft is approved, the daemon must treat that persisted action as granted for
notification ordering even if the global gate remains in approval mode:
deliver the delegated owner notice first, then consume the exact approval and
send. Approval notices should also name assistant-facing drafts as assistant
replies rather than delegated replies.
