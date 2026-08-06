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
- sender-facing output may be sent only when it was generated and validated in
  the current online session, unless it is an exact owner-approved action whose
  approval record is the authority.

Testing must reproduce two process sessions against one state database and
assert both the negative send count and absence of old task/context hydration.
