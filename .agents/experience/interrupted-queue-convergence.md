# Interrupted queue convergence

Interrupted count is an audit signal, not a backlog that should be replayed in
bulk.

- Classify exact work using persisted event direction, latest model/tool stage,
  Lark thread context, approval state, and external-action certainty.
- Keep only work with a current, verifiable business result still worth
  delivering.
- Cancel stale, superseded, misclassified, and acceptance-test work with an
  explicit reason. Preserve receipts, runs, steps, decisions, actions, and
  interruption snapshots.
- Never cancel or replay an executing or result-uncertain external action.
- Approval from an older session can requeue only into the newest active
  session after that session is ready. A newer starting session fences every
  older ready record.
- Lifecycle interrupted counts should converge through explicit resume or
  audited cancellation, never physical deletion.
