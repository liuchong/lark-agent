# Bounded convergence preserves validated drafts

- A final conversation-context defer must not discard a reply that already
  passed language, identity, permission, quality, evidence, and grounding
  checks. Persist it before the semantic recheck and re-run only that recheck.
- Candidate creation and every active-state transition must be fenced by the
  current work lease, and a reclaimed candidate must pass current routing
  policy before semantic resolution. Otherwise an expired worker can alter a
  newer draft or a policy change can leak an old reply. Semantic context
  retries need their own persisted counter so provider failures cannot
  prematurely exhaust the context-only budget.
- `evidence_status` and `reply_outcome` answer different questions:
  `evidence_status` limits which facts may be claimed; `reply_outcome` says
  whether the current response is complete, partial, or needs clarification.
- Deterministic schema, quality, evidence, permission, and repeated-input
  failures belong to the current model run. Repair, narrow, or converge there;
  do not turn them into a general queue retry that repeats the investigation.
- Prompt guidance is explanatory. Every tool, path, permission, budget,
  evidence, and send restriction needs a matching Go rejection path.
- Sender-facing authority must be recomputed from the original event and the
  current deterministic router immediately before every reply or approval.
  Model output, candidate metadata, and persisted approval relevance are not
  authority. If routing no longer permits a send, cancel the candidate and
  lease-fence any ready approval into a blocked audit state before completing
  the work as ignore, record, or notify.
- Failure fingerprints must exclude volatile receipt metadata. Bind the tool,
  normalized arguments, stable error class, and bounded result content so
  unchanged conditions are detected reliably.
- Owner-visible terminal summaries may include only validated candidates or
  deterministic receipt-derived progress. Rejected model prose remains audit
  history and must not be copied into a message.
