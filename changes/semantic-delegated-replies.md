# Semantic Delegated Replies

## Goal

The delegated-reply workflow covers every allowed group message that mentions
the configured owner and evaluates every inbound human private message to the
owner as a semantic candidate. It waits three minutes before doing reply work,
then uses same-chat semantics to decide which messages need an owner response
and which pending requests the owner actually handled. Only requests that still
need a response may enter the read-only reply workflow.

## Directly Applicable Rules

- "所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按
  red -> green -> refactor 实现。"
- "每项逻辑变化必须有 `integration_test/` 覆盖；没有相应集成测试时不得宣称完成。"
- Lark production calls remain inside `internal/lark` and use only the official
  public Go SDK.
- Non-owner work remains read-only and workspace-bounded.
- Verification and commit complete before installation or restart.

## Business Design

### Core Data

Every delegated candidate is a normal durable `WorkItem` with:

- `status=waiting_user` while the owner grace period is open;
- `next_attempt_at` equal to the latest trusted create/update time plus
  `policy.owner_wait`;
- delegated relevance selected by deterministic routing:
  `direct_mention` for group `@Owner`, or `private_message` for an inbound human
  P2P message;
- one or more immutable semantic-resolution audit rows.

Each resolution row records the target work item, result
`answered|unanswered|no_reply_needed|ambiguous`, matched owner message IDs,
confidence, context cutoff, reason, and evaluation time. It contains message
IDs and digests, not credentials or unbounded message bodies.

### Decision Rules

1. Bot/app messages, owner-authored non-assistant messages, blocked
   senders/chats, and non-owner messages sent directly to the assistant bot do
   not become delegated candidates. Polling drops an owner-authored
   non-assistant message before durable intake, so later lexical relevance
   inference cannot re-admit it.
2. An allowed group `@Owner` or inbound human P2P message enters
   `waiting_user`. Assistant-facing owner requests remain immediate.
3. Waiting work is not claimable before `next_attempt_at`; no worker, lease, or
   main reply-model call is held during the grace period.
4. At the deadline, the resolver reads a bounded, paginated same-chat window
   containing one owner-wait interval before the earliest pending target, the
   target, related pending targets, intervening discussion, and owner-authored
   messages after the target. This pre-target slice lets the resolver
   distinguish a new request from an answer to a question initiated by the
   owner.
5. Reply/thread relations and adjacency are evidence, not a terminal answer.
   The resolver must decide whether owner content substantively handles the
   exact target. For an ordinary private message without explicit `@Owner`, it
   also decides whether the message reasonably requires any response.
6. A high-confidence `answered` result cancels only that target. A
   high-confidence `no_reply_needed` result cancels an answer to an
   owner-initiated question, acknowledgement, reaction, or ordinary
   conversational continuation that adds no new request. Explicit group
   `@Owner` work cannot use this result. A high-confidence `unanswered` result
   admits only that target to reply work. `ambiguous`, malformed, unavailable,
   or low-confidence results fail closed and return the target to
   `waiting_user` for a bounded retry. When the configured retry ceiling is
   reached, the same transaction moves the target to dead letter, clears its
   lease and next-attempt time, records the exact terminal reason, and writes a
   durable owner-resolution requirement.
7. The main reply context includes post-target discussion, while the verified
   semantic-resolution record remains in the audit ledger. The reply must still satisfy the existing useful-work,
   evidence, commitment, and non-owner read-only gates.
8. Immediately before beginning the durable reply action, the resolver checks
   messages newer than its prior context cutoff. New relevant owner content
   cancels or delays the reply; no new owner content permits the draft to
   proceed.

### State Flow

`received -> waiting_user -> processing`

- `answered -> cancelled(owner_semantically_replied)`
- `no_reply_needed -> cancelled(delegated_reply_not_needed)`
- `unanswered -> routed/model -> reply|record|notify|approval`
- `ambiguous/error -> waiting_user(next semantic retry)`
- `ambiguous/error at retry ceiling -> dead_letter(owner resolution summary)`
- `withdrawn -> cancelled(message_withdrawn)`

An edit before send replaces the target content used for future evaluation and
moves the deadline to `max(create_time, update_time) + owner_wait`.

### Restart Boundary

A waiting candidate has not run the reply model and has no external action.
After restart it may be re-admitted only by a fresh Lark read and fresh
semantic evaluation; no old draft or old semantic result is replayed. Work that
had entered model, approval, or external-action stages keeps the existing
interrupted/explicit-resume contract. Uncertain external actions are never
retried automatically.

### Configuration And Installation

- `policy.reply_scope`: `all_groups|configured_groups` for group `@Owner`.
- `policy.private_reply_scope`: `all_private|disabled` for inbound human P2P.
- `policy.owner_wait`: grace period, default and installed value `3m`.
- `policy.owner_reply_confidence_min`: minimum semantic confidence.
- `policy.owner_reply_retry`: delay after ambiguous or failed evaluation.
- Existing allow/block chat and user lists remain mandatory.

The production installation uses `all_groups`, `all_private`, and `3m`.
Doctor and help expose all effective values without exposing message content.

### Failure And Safety

- A semantic model or Lark history failure never means "unanswered".
- Repeated incomplete or ambiguous context cannot remain in `waiting_user`
  forever. Retry exhaustion sends one private, localized, actionable summary
  to the Owner and never posts a late reply to the original chat.
- The dead-letter transaction and owner-summary requirement commit together.
  Immediate handling, periodic maintenance, and startup recovery consume the
  requirement idempotently, so a crash before notification action creation
  cannot leave an invisible terminal tail.
- Resolver output is strict JSON. Every matched ID must be an owner-authored,
  same-chat message newer than the target; unknown IDs reject the result.
- Context is time-, count-, and byte-bounded. An incomplete window is
  `ambiguous`, not permission to send.
- The resolver has no tools and cannot perform external actions.
- The subsequent delegated run keeps the sender-derived read-only tool catalog,
  same-chat boundary, workspace sandbox, and commitment approval rules.
- Lark has no compare-and-send primitive. The final incremental check minimizes
  but cannot eliminate the interval between the last read and the send call.
- Durable investigation messages keep their complete internal action keys for
  audit, but the UUID passed to Lark is a stable stage-distinct digest no longer
  than 50 characters.

## BDD Acceptance

### Scenario: Group mention waits for the owner

Given an allowed human message mentions the owner in any allowed group,
when less than three minutes have elapsed,
then the item remains `waiting_user`,
and neither semantic nor reply model is called.

### Scenario: Ambiguous context reaches a bounded terminal state

Given a delegated target repeatedly has incomplete or ambiguous semantic
context,
when the final configured retry is deferred,
then the target becomes dead letter with no next attempt,
one private Owner summary names the work, `/task <id>`, and
`/task resume <id> confirm`,
and no delayed response is posted to the source conversation.

### Scenario: Crash before terminal notification action is recoverable

Given the final semantic deferral commits its dead letter,
when the process exits before beginning the private notification action,
then the same transaction has already persisted an owner-resolution
requirement,
and periodic or startup recovery sends the idempotent private summary once.

### Scenario: Resume cancels an unsent terminal generation

Given terminal work has an unsent owner-resolution requirement,
when the Owner explicitly resumes that terminal work before its send action
begins,
then the resume transaction cancels the old requirement,
and later maintenance does not send a stale stopped-work summary.

### Scenario: A later terminal generation is independent

Given one terminal summary completed and the Owner explicitly resumed the work,
when the same work later reaches dead letter again,
then a distinct outbox generation and distinct Lark idempotency key are used,
and no action from the earlier terminal generation can complete the later one.

### Scenario: Explicit resume distinguishes failed from uncertain sends

Given an owner summary send has a known failed result,
when the Owner explicitly resumes the terminal work,
then that failed summary is cancelled as superseded and is not retried;
but an executing summary with an uncertain Lark result blocks resume.

### Scenario: Investigation messages satisfy Lark UUID limits

Given a durable investigation uses a full action key longer than 50 characters,
when the Owner notice and source-thread progress message are sent,
then Lark receives stable distinct UUID digests of at most 50 characters,
while the full keys remain available in the local audit record.

### Scenario: Parallel tool rejections do not exhaust one coding run

Given one coding model turn emits a useful bounded lookup together with a
policy-rejected shell search,
when the no-progress budget is updated,
then the whole model turn records progress rather than charging each sibling
tool call separately.

Given the model later sends a malformed free-form investigation plan,
when the plan tool rejects it,
then the error names the required structured fields,
and a corrected plan plus bounded search can complete in the same run.

Given all successfully parsed bounded searches for a false-premise coding
question return zero matches,
when the insufficient-evidence decision is normalized,
then the runtime renders a receipt-backed not-found summary with the actual
queries, scan counts, and truncation states,
and never claims global nonexistence or preserves unrelated model inference;
missing, null, wrongly typed, or negative scan metadata and more than sixteen
receipts, including repeats, fall back to the fixed conservative template
instead of inventing or hiding evidence; a bare optional code-index miss
without a bounded scan receipt follows the same rule.

### Scenario: Every inbound human private message is evaluated

Given an inbound human P2P message to the owner that is not the assistant chat,
when private reply scope is `all_private`,
then it follows the delegated waiting and semantic-evaluation path, and only an
outstanding response need may continue to reply work.

### Scenario: Owner-authored human private message never becomes delegated work

Given the owner asks another human a question in private chat,
when user-token polling sees that outgoing message,
then it is discarded before durable intake,
and inferred relevance cannot send a reply to the owner's own message.

### Scenario: Answer to owner-led discussion needs no synthetic reply

Given another human answers an owner-initiated private-chat question without a
new request and the owner continues the same discussion,
when the semantic deadline is evaluated,
then the resolver returns `no_reply_needed`,
and no reply-model call or sender-facing reply occurs.

### Scenario: Genuine unanswered private request remains eligible

Given another human privately asks a new question or makes a request that the
owner has not handled,
when the semantic deadline is evaluated,
then the resolver returns `unanswered` and the read-only delegated workflow may
continue.

### Scenario: Non-owner assistant invocation stays silent

Given a non-owner privately messages or mentions the assistant bot,
when intake and routing run,
then no delegated candidate or model run is created.

### Scenario: Unquoted semantic answer suppresses one target

Given two pending requests in one chat and an unquoted owner message that
substantively answers only the first,
when both candidates are resolved,
then only the first is cancelled and the second remains eligible for reply.

### Scenario: Owner discussion is not necessarily an answer

Given the owner sends later messages in the chat,
when those messages discuss another topic or do not answer the target,
then the resolver marks the target unanswered and the agent may reply.

### Scenario: One owner message answers multiple targets

Given one owner message substantively answers two pending requests,
when each target is resolved against the same bounded context,
then both targets are cancelled with the same validated owner message ID.

### Scenario: Ambiguity fails closed

Given the context is truncated, the model response is malformed, or semantic
confidence is below the configured threshold,
when the deadline is evaluated,
then no sender-facing reply is sent and the item receives a delayed retry.

### Scenario: Final check catches a late owner answer

Given a target was judged unanswered and a draft was produced,
when the owner answers before the reply action begins,
then the final semantic check cancels the action and no reply or owner notice is
sent.

### Scenario: Edit, withdrawal, restart, and approval

Given a target is edited, withdrawn, waiting during restart, or held for
approval,
when it next approaches a send boundary,
then the latest same-chat state is read; edits reset the grace deadline,
withdrawals cancel, waiting candidates are freshly evaluated, and old
model/action work is never replayed automatically.

### Scenario: Delegated permissions do not widen

Given a group or private delegated candidate was sent by a non-owner,
when semantic resolution and the main agent run execute,
then the resolver has no tools and the main run exposes only explicitly
read-only same-chat/workspace tools.

## Test Locations

- `agent/router`, `agent/poll`: group/private eligibility and silence boundaries.
- `agent/storage`: migration, waiting deadlines, claim eligibility, audits, and
  restart behavior.
- `internal/lark`: paginated post-target windows, edit/withdrawal projection.
- `agent/runtime`: strict semantic prompt/output validation.
- `agent/app`, `agent/policy`, `agent/reply`: model-before/model-after gates and
  final incremental check.
- `integration_test/lark_agent`: executable end-to-end scenarios for timing,
  multiple targets, private routing, ambiguity, restart, and permissions.

## Non-Goals

- Reading private messages addressed to the assistant bot from non-owners.
- Replying to ordinary group messages that do not mention the owner.
- Giving non-owner-triggered work mutation, shell, cross-chat, deployment, or
  arbitrary messaging authority.
- Automatically replaying old model drafts or uncertain external actions.

## Completion And Stop Conditions

The work completes when specifications, red/green tests, production code,
configuration, help, docs, AI-facing records, full verification, independent
review, commit/push, production install, and bounded live Lark evidence agree.
Absence of a controllable second human identity must be reported rather than
papered over with a bot-sender test.
