# Cross-session reply authority

A persisted model context or reply candidate is audit evidence from the online
session that created it. It is not authorization for a later daemon process to
send.

For delegated conversation work:

- startup recovery leaves persisted investigation context and unsent reply
  candidates interrupted;
- a held candidate is inspectable but never claimable across sessions;
- explicit owner resume archives the prior investigation, cancels its draft,
  and re-runs current routing and semantic classification from the original
  target;
- completed replies, owner notifications, and staged investigation messages
  remain immutable audit evidence for their original communication generation,
  but cannot short-circuit or provide idempotency identity to an explicitly
  resumed generation;
- absence of a current-generation approval action means "not pre-approved",
  not a storage failure; automatic low-risk replies must continue through the
  normal policy gate, while approval-required replies still create and await a
  new generation-scoped approval;
- mutating resource/tool actions keep independent idempotency fences so a new
  communication generation never implies replaying side effects;
- CLI and Owner-private resume commands must share one write-first transaction
  that re-reads status and generation under the lock; otherwise concurrent
  resumes or divergent control paths can reuse old authority;
- a blocked action derived from an executing interruption remains
  result-uncertain until reconciliation and cannot be cancelled as an ordinary
  failed communication during resume;
- sender-facing output may be sent only when it was generated and validated in
  the current online session, unless it is an exact owner-approved action whose
  approval record is the authority.

Testing must reproduce two process sessions against one state database and
assert both the negative send count and absence of old task/context hydration.
