# Interrupted queue convergence

## Goal

Operators can finish a bounded audit of interrupted work instead of leaving
every historical receipt visible as unfinished forever. Useful work remains
resumable; stale, misclassified, superseded, or test work becomes durably
cancelled.

## Business design

### Data and state

- `queue cancel` selects either explicit work/message IDs or all currently
  interrupted work.
- `--keep-work-id` is valid only with `--all-interrupted` and excludes audited
  useful items from the batch.
- Every cancellation requires a non-empty operator reason.
- Cancellation changes a safe work item to `cancelled`, clears leases and retry
  timing, blocks any active coding goal, closes its unresolved interruption,
  cancels unsent `awaiting_approval` or `ready` actions, and appends a completed
  `operator_cancel` action containing the reason.
- Existing runs, steps, decisions, message receipts, and action evidence remain
  intact.

### Safety and failure behavior

- Safe cancellation states are `received`, `routed`, `waiting_user`, `ready`,
  `awaiting_approval`, `retry_wait`, and `interrupted`.
- A batch fails atomically if any selected item is missing, terminal, currently
  processing/executing, has an executing action, or has an unresolved
  interruption whose action was executing.
- Cancellation never sends a Lark message and never physically deletes queue
  history.
- Empty selectors, mixed exact/all selectors, `--keep-work-id` without
  `--all-interrupted`, and empty reasons fail before storage mutation.

### Approval handoff

- Approving an exact action changes the action to `ready`.
- If the newest active daemon session is ready, approval assigns the work item
  to that session and changes it to `received`.
- If the newest active session is still starting, or no active session exists,
  the work remains `interrupted`; the approved action stays ready for a later
  explicit `queue resume`.
- Rejection cancels the action and work and blocks an associated coding goal.

### Forced decision convergence

- Once tool, repeated-call, no-progress, or citable-evidence gates require a
  terminal decision, subsequent model requests expose only `submit_decision`
  and include an explicit system instruction that earlier tools are no longer
  available.
- The model gets at most three terminal-only attempts. If it still emits plain
  text or calls an unavailable investigation tool, the run fails with the
  explicit `model_non_convergence` subtype instead of consuming the remaining
  general turn budget.
- `model_non_convergence` moves the exact leased work directly to dead letter
  with its reason and audit history. Network, rate-limit, and other retryable
  failures retain the existing bounded retry policy.
- No failed convergence path fabricates a reply or external action.

### Installation and defaults

This change adds no configuration and changes no automatic replay policy.
Cross-session work still runs only after an exact approval handoff or explicit
resume. Installation preserves the existing SQLite data and applies no
destructive migration.

## BDD acceptance

- Given interrupted historical work and two audited useful work IDs, when the
  owner runs `queue cancel --all-interrupted` with both IDs in
  `--keep-work-id` and a reason, then all other interrupted work becomes
  cancelled and the two useful items remain unchanged.
- Given a selected item has an executing or result-uncertain external action,
  when cancellation is requested, then the whole batch fails and no selected
  work or action changes.
- Given an interrupted item has an unsent approval, when it is cancelled, then
  the approval becomes cancelled, the interruption is closed, and inspectable
  audit contains the operator reason.
- Given an approval belongs to an older daemon session and a newer session is
  ready, when the owner approves it, then the exact persisted draft is assigned
  to the newer session as received and can be claimed without model rewriting.
- Given the newest daemon session is still starting or no daemon session is
  active, when the owner approves an interrupted action, then the draft becomes
  ready but the work remains interrupted until an explicit resume.
- Given invalid selector combinations or a blank reason, when `queue cancel`
  runs, then it fails without changing state.
- Given a model reaches a forced-decision gate, when it calls an old
  investigation tool once and then submits a valid decision, then the old tool
  is not executed and the decision completes.
- Given a model repeatedly ignores terminal-only instructions, when three
  terminal-only attempts are exhausted, then the run fails before the general
  turn limit, the leased work moves directly to dead letter, and no reply or
  action is fabricated.

## Tests and fixtures

- Storage tests create interrupted, kept, approval, executing, uncertain, and
  cross-session fixtures and assert atomic state transitions.
- Command tests assert selector validation and structured changed-work output.
- `integration_test/lark_agent` runs the built binary against a temporary
  SQLite database and verifies batch cancellation plus approval reassignment.

## Documentation

`spec/behavior.md`, command help, and `docs/operations.md` describe the same
selection, audit, safety, and approval handoff behavior.

## Non-goals

- No automatic semantic decision chooses which historical work is useful.
- No queue row, receipt, run, step, or action history is deleted.
- No uncertain external action is reconciled by this command.
- Queue pagination and historical export redesign are outside this change.
