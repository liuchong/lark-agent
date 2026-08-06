# Interrupted queue convergence

Interrupted count is a transient recovery signal, not a durable backlog.

- Classify exact work using persisted event direction, latest model/tool stage,
  Lark thread context, approval state, and external-action certainty.
- Automatically readmit safe read-only/model work into the new ready session;
  current evidence and ordinary routing decide whether it replies or ignores.
- Preserve exact approval waits and send one durable owner instruction.
- Terminalize result-uncertain external actions and send one separately fenced
  reconciliation notice. Preserve all audit history.
- Never cancel or replay an executing or result-uncertain external action.
- Approval from an older session can requeue only into the newest active
  session after that session is ready. A newer starting session fences every
  older ready record.
- A forced terminal decision needs its own small hard attempt bound. Hiding old
  tools is insufficient when a provider still emits calls to them.
- Repeated terminal-only protocol refusal is permanent for that run and must
  dead-letter immediately; treating it as transport retry just creates a
  shorter loop.
- Lifecycle counts must report actual convergence outcomes rather than leaving
  an unactionable interrupted total.
- When a sender-facing message says the named owner was notified, persist and
  complete that owner notice first. On recovery, a completed notice alone must
  never skip the sender-facing reply; finish-only recovery additionally
  requires a completed durable reply action.
