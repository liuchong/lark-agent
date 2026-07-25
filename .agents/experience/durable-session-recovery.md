# Durable session recovery experience

Reusable verified rules:

- Observation time cannot determine whether delayed transport data belongs to
  the current run. Persist the source event time and compare it with the online
  session boundary.
- Public timestamps may have whole-second precision. Compare them with a
  session boundary truncated to the same precision instead of rejecting a
  message created during the startup second.
- Append a receipt for every duplicate observation, but keep one work item per
  message ID. Hydrating a richer duplicate snapshot must not reset terminal
  status.
- Database connections used by doctor, status, inspect, approval, or schema migration
  must not own the daemon session; closing them must not stop the daemon.
- A multi-worker daemon must serialize SQLite access through one connection.
  Configure a bounded busy timeout before WAL so a short writer held by an
  operator process delays durable transitions instead of failing one worker
  immediately. Do not add unbounded application retries around external action
  state.
- Every claim needs a unique token, not only a worker name. Completion, retry,
  heartbeat, Goal writes, and side effects must fence on that token.
- Persist external-action intent before the call. If the process stops while the
  action is executing, retain uncertainty and require reconciliation or explicit
  owner action rather than automatically retrying.
- During intentional shutdown, cancel workers first, snapshot current-session
  unfinished work, send the durable offline notice, then stop the session.
- A generic retry command must not become a second recovery path. It may only
  accelerate current-session `retry_wait` work with no executing or blocked
  action; prior-session, interrupted, processing, and terminal work remains
  behind exact inspect/resume gates.
- SQLite schema migration runs inside the owned state database. It must be
  bounded by the store connection and must not imply historical data import.
