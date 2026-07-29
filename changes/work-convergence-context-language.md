# Work convergence, context budget, and localized assistant identity

## Goal

Every accepted work item must keep moving toward one meaningful outcome.
Restart recovery must automatically resume work that is safe to recompute,
surface exact owner actions for work that needs human input, and terminalize
stale or externally uncertain work without replaying uncertain side effects.

Every model request must expose both model-turn and model-visible context
budgets. The runtime must compact older evidence before the context limit is
exhausted and make the model converge as either budget becomes scarce.

Every outward message must use one resolved natural language. Delegated replies
must visibly identify the intelligent assistant, say that the named owner was
notified, and never call that person a generic "user".

## Business design

### Configuration

`owner` gains:

- `name`: the human name used in delegated replies and private notices;
- `preferred_language`: `auto`, `zh-CN`, or `en-US`;
- `fallback_language`: `zh-CN` or `en-US`.

`preferred_language` wins when it is not `auto`. In auto mode, the current
message and bounded same-chat context are scored by script. Han text resolves
to `zh-CN`, Latin text resolves to `en-US`, and an inconclusive score uses
`fallback_language`. Existing installations load with safe defaults; the
current Liu Chong installation explicitly uses `name: 测试负责人`,
`preferred_language: zh-CN`, and `fallback_language: zh-CN`.

`agent.context_compaction_ratio` controls the soft compaction threshold and
defaults to `0.80`. The hard `max_context_bytes` remains the final bound.

### Model budget and compaction

Before every model request the runtime calculates:

- current one-based model turn, total turns, and turns remaining after the call;
- current model-visible bytes, configured byte limit, remaining bytes, and
  utilization percentage;
- whether automatic compaction occurred and how many old messages it replaced.

At 80% of either finite budget the prompt requires narrow verification and
convergence. In the final two turns, or when context headroom cannot safely fit
another ordinary tool result, only a terminal decision is allowed.

Automatic compaction is deterministic and invokes no second model. It preserves
the system contract, original task, safety and permission boundaries, source
references, durable external-action receipts, explicit unknowns, and the newest
conversation. Older model/tool exchanges become one structured checkpoint.
Raw repeated output and obsolete reasoning are dropped. A checkpoint records
that compaction happened, so later turns cannot treat removed text as new
evidence or restart the same broad investigation.

### Language and assistant identity

The resolved language is part of the bounded user profile and every turn
prompt. Terminal validation rejects a sender-facing reply whose prose is
predominantly in another language; model repair occurs inside the existing
bounded loop. Identifiers, file names, error codes, URLs, and quoted source
fragments may remain unchanged.

The model supplies useful business content only. For a delegated private
message or direct owner mention, the runtime deterministically renders:

`🤖 智能助手：<useful result>`

and a same-language disclosure that the concrete configured owner name was
notified. This wrapper is not used for the owner's direct bot requests. It is
applied before the durable reply action so retries reuse identical text.

Private owner notices never paste the model's internal English reason. They use
localized templates, concrete work/message identifiers, completed checks or
known failure categories, and an exact next action where one exists.

### Recovery convergence

At ready startup, before workers claim normal work:

1. Interrupted work with no uncertain external action is reassigned to the
   current session and made claimable. Read-only/model work may recompute from
   current evidence.
2. An unsent approval or other exact owner input remains durably waiting, but
   the owner receives one idempotent private message with the work ID and exact
   approval or inspection command.
3. Work whose external action was executing at interruption is never replayed.
   It moves to dead letter with the uncertainty snapshot and receives one
   idempotent private reconciliation notice.
4. A withdrawn, duplicate, stale, or no-longer-relevant item is allowed to pass
   through the ordinary router/gates and terminalize as ignored or cancelled.
5. Retry exhaustion and model non-convergence also create one durable owner
   resolution notice. The notice says what was attempted, why processing
   stopped, and the exact inspection/resume command if retry is useful.

Lifecycle notices report what the startup reconciler actually did: resumed,
waiting for owner, terminalized, and uncertain counts. They do not leave a
generic interrupted count with no next action.

### Persistence and migration

No work, receipt, run, step, decision, or action history is deleted. Recovery
updates the existing work status/session and closes the corresponding
interruption snapshot. Owner-resolution notifications use the existing durable
action-attempt table with a distinct idempotent action kind. No SQL schema
migration is required; existing databases are updated through ordinary state
transitions.

### Permissions and silence

Recovery never broadens tool authority. Work triggered by a non-owner remains
read-only after resume. Private non-owner messages to the bot and non-owner
direct bot mentions remain silent under the existing routing contract.
Uncertain writes, replies, notifications, shell commands, and other external
actions are not replayed.

## BDD acceptance

1. Given safe interrupted read-only work from a previous session, when a new
   session becomes ready, then the work is reassigned and processed without an
   operator `queue resume`.
2. Given an interruption snapshot records an executing external action, when
   startup converges the queue, then the action is not executed again, the work
   becomes terminal, and the owner receives one reconciliation notice.
3. Given work requires an exact owner approval, when startup converges the
   queue, then the draft remains unchanged and the owner receives one message
   naming the work ID and exact approval/inspect action.
4. Given retries or terminal-only model attempts are exhausted, when the work
   enters dead letter, then the owner receives one durable useful summary and
   later restarts do not duplicate it.
5. Given a 150-turn run at turn 121 and 83% context utilization, when the model
   is called, then its prompt includes total/current/remaining turns, used/limit/
   remaining context, compaction status, and an urgent convergence instruction.
6. Given old tool results push context past the soft threshold, when the next
   request is built, then old exchanges become one checkpoint preserving source
   and action receipts while the newest messages remain intact.
7. Given `preferred_language: zh-CN`, when the model submits an English prose
   reply, then terminal validation rejects it for repair and no mixed-language
   Lark message is sent.
8. Given language is `auto` and bounded conversation is predominantly Chinese,
   when a lifecycle, delegated, or private owner message is rendered, then all
   explanatory prose is Chinese.
9. Given another person directly mentions the owner and the assistant completes
   relevant work, when the reply is sent, then it starts with `🤖 智能助手：`,
   includes a useful finding, and says that the configured owner name was
   notified. The durable private owner notice completes before the sender-facing
   reply; a known notice failure blocks that reply, and an uncertain interrupted
   notice is not replayed.
10. Given the owner directly asks the bot a question, when the bot replies, then
    the delegated-owner disclosure is not added.
11. Given the owner name is unavailable, when a delegated reply would be sent,
    then the sender-facing action is blocked and the owner receives a
    configuration instruction; the text never substitutes the word "用户".

## Test and fixture map

- Runtime unit tests cover budget prompts, semantic checkpoint compaction,
  language validation, and terminal convergence.
- App/reply tests cover deterministic assistant identity decoration,
  idempotency, owner-name failure, and owner-notice behavior.
- Storage/session tests cover all recovery classifications and durable
  resolution notification fencing.
- Lifecycle tests cover localized, action-oriented summaries.
- `integration_test/lark_agent` adds process-level scenarios for migration,
  recovery convergence, context budgeting, language, and delegated identity.
- Live validation uses the international Lark installation and only the
  explicitly authorized assistant/private chat and group mention paths.

## Documentation and installation

Update `README.md`, configuration, operations, macOS installation examples,
command help, long-term `spec/behavior.md`, and AI-facing recovery experience.
The installer preserves secrets and existing state, writes the explicit current
owner language/name configuration, then validates, commits, pushes, installs,
restarts, and performs bounded live Lark checks.

## Non-goals

- No automatic replay of externally uncertain actions.
- No deletion of historical audit rows.
- No general translation service or unbounded locale catalog.
- No change to non-owner write permissions or private-bot silence rules.
- No hardcoded bot name or owner name in reusable production logic.
