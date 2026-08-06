# Semantic delegated reply window

Reusable verified rules:

- Put the owner grace period in the durable queue. A worker or reply policy
  must not sleep through the grace period or add a second wait before send.
- Decide one exact target at a time while supplying every active same-chat
  target. Adjacency, quoting, and any later owner message are evidence, not
  proof that the exact target was answered.
- Read the target exactly and paginate same-chat messages from newest to the
  earliest pending target. Missing targets mean withdrawn; truncated windows,
  malformed model output, and low confidence fail closed.
- Validate every matched message ID against supplied same-chat,
  owner-authored, post-target messages. The semantic matcher has no tools.
- In a group, an unthreaded direct owner mention from another sender starts a
  newer conversation segment. Exclude that boundary and its later unlinked
  messages from both semantic classification and the delegated Agent context;
  otherwise a valid owner reply to the newer request can suppress or redefine
  an older unrelated target.
- Re-read before the external reply action. Lark has no compare-and-send
  primitive, so the final semantic read is the last enforceable boundary.
- Once that semantic gate classifies an exact target as unanswered and admits
  it to the main model, terminal validation must reject `ignore`, `record`, and
  `notify`. Otherwise a later model can silently override the exact-target
  decision by treating pre-target owner participation as current handling.
  Legitimate withdrawal, owner handling, and no-reply private continuations
  converge before the main model runs.
- Use the target's trusted update time as a new grace-period origin. Exact
  target hydration must enforce this even when polling does not rediscover the
  edit.
- Only pristine `waiting_user` work with no model run or action attempt may
  cross a daemon restart. Reassign it to the new session and re-read Lark;
  never replay an old draft, approval, or uncertain action.
- Keep group owner mentions, inbound human private messages, and native
  assistant invocations as separate configured scopes and routing identities.
- Apply the recovery retry ceiling to semantic `waiting_user` deferrals too.
  The final exact lease must atomically become dead letter, clear its future
  attempt, and enter the existing owner-resolution path; a special waiting
  state must not bypass the queue's convergence bound.
- Keep complete action idempotency keys in local audit storage, but derive a
  stable, domain-prefixed digest for public Lark message UUIDs. Public API
  limits are transport contracts and must not truncate or redefine the
  internal uniqueness key.
- Bind terminal owner summaries to a durable terminal-generation ID, not only
  a work item ID or message digest. Resume must cancel unsent requirements,
  reject result-uncertain sends, and let a later dead-letter generation create
  a distinct requirement and public idempotency key.
- Charge no-progress budget once per model turn, not once per sibling tool
  call. A useful successful call in a mixed turn resets the streak; otherwise
  parallel policy rejections can consume an entire run before the model can
  correct a plan. Validation errors for structured planning tools must name the
  exact required fields so bounded self-correction is possible.
