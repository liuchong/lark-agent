# Mature Multi-Provider Agent Runtime

## Problem

The current foreground agent runtime is wired to one OpenAI Chat Completions
compatible adapter and forces tool use through `tool_choice=required`. For Kimi
thinking-enabled models this can fail before the model performs any work. The
failure is then treated like ordinary bounded retry work, so the queue may spend
retries without producing a useful partial conclusion, exact blocker, or
provider diagnosis.

The same runtime also lacks a durable local contract for provider turns,
role-specific model profiles, per-step retry classification, stream liveness,
stable prompt-prefix accounting, code-maintained run state, and regression
fixtures from real failed trajectories.

## Behavior

`lark-agent` will use a local model-runtime contract between the Agent loop and
provider wire protocols.

The supported roles are:

- `agent`: main tool-using investigation and reply decision loop;
- `semantic`: owner-handled and no-reply-needed classification;
- `finalizer`: no-tool terminal summarization when the main loop refuses to
  submit a terminal decision;
- `compactor`: no-tool context compaction for low-risk historical tool text;
- `vision`: optional image understanding.

Each role binds to a named model profile. A profile declares provider,
protocol, endpoint, model name, credential reference, reasoning behavior,
streaming behavior, timeout, and capability limits. A role may share the same
profile as another role, but the audit log must record the actual role,
profile, provider, protocol, and model used.

Provider adapters translate between the local request/turn types and:

- OpenAI-compatible Chat Completions, including Kimi provider traits;
- OpenAI Responses without server-side session state;
- Anthropic Messages.

For Kimi thinking-enabled Chat Completions, the runtime supplies tool
definitions but does not send forced `tool_choice=required` by default. A model
may decide whether to call tools or submit a terminal decision. Forced tool
choice can only be used by an explicit profile whose reasoning mode is known to
support it.

## Core Data Model

The new model-runtime package owns:

- `Profile`: provider, protocol, base URL, model, credential reference,
  timeout, stream mode, reasoning mode, context/output limits, and capability
  flags.
- `RoleBindings`: mapping from runtime roles to profile names.
- `Request`: canonical messages, tool definitions, tool-choice intent,
  structured-output requirement, stable prompt cache key, run state, and
  budgets.
- `Message` and `Block`: text, images, tool calls, tool results, and
  provider-private thinking continuity blocks. Thinking continuity is replayed
  only inside the current run and is never exported.
- `Turn`: assistant text, completed tool calls, finish reason, request ID,
  usage, cache metrics, and provider metadata.
- `Failure`: typed failure category, HTTP status when available, retryability,
  retry-after delay, recovery action, and redacted diagnostic string.
- `RunState`: original goal, current phase, completed checks, verified sources,
  exact unknowns, tool/repetition counters, model-turn budget, per-step attempt,
  context budget, wall-clock budget, last failed gate, and legal next actions.

## State Flow

The main loop becomes an explicit phase machine:

1. `prepare`: select role profile, render stable prompt and current state.
2. `generate`: call the provider adapter through the local model contract.
3. `validate_turn`: check protocol shape, finish reason, tool IDs, arguments,
   and structured output.
4. `execute_tools`: execute only allowed tools under current sender authority,
   risk, concurrency, lease, idempotency, and workspace boundaries.
5. `observe`: record typed tool receipts and source references.
6. `verify_progress`: update code-maintained run state and no-progress
   counters.
7. `converge`: narrow or require `submit_decision` when evidence, budget, or
   repeated-failure gates say investigation should stop.
8. `finalize`: call no-tool `finalizer` only after terminal-only refusal.
9. `terminal`: apply quality, evidence, grounding, permission, approval, owner
   handled, routing, and send gates.

Events are consumed only at safe phase boundaries. Normal follow-up events wait
until a complete assistant/tool protocol unit exists. Urgent cancellation or
withdrawal may stop a step, but it must not fabricate tool success or persist
half-finished thinking.

## Context And Prompt Design

The stable core prompt, task-process prompt, and tool schema are rendered
deterministically with stable ordering and recorded hashes. Dynamic data such
as time, budgets, current phase, retry attempt, exact failures, and recent
events is appended near the end of the request as a run-state reminder.

The run-state reminder is computed by Go from durable state and tool receipts.
The model never maintains counters, verified sources, or the list of completed
checks. External web pages, chat text, tool free text, and summaries are
untrusted evidence and cannot be promoted into the high-trust state fields.

Context compaction is layered:

1. bound each oversized tool result and preserve preview, digest, and source
   references;
2. delete clear noise;
3. replace older complete protocol units with deterministic checkpoints;
4. allow `compactor` to summarize only low-risk historical tool text.

Compaction may never drop the original user goal, permissions, source
references, completed checks, exact unknowns, failure classes, modified-file
records, or rollback notes. A compactor failure preserves the deterministic
checkpoint and cannot start a recursive model recovery loop.

## Error And Retry Rules

Failures are classified before retry:

- Non-retryable: HTTP 400/401/403/404, quota exhausted, invalid provider
  output, profile/protocol capability mismatch, schema mismatch, unsupported
  tool choice, and permission denial.
- Retryable inside the current model step: network transport errors, timeout,
  empty response, HTTP 408/409/429, server 5xx, and 529 overload. `Retry-After`
  is honored when present.
- Changed-input recovery: context overflow, request too large, unsupported
  image format, or output truncation. Each changed-input recovery has its own
  small limit.

A failed transport attempt does not consume a model turn. A deterministic
provider failure does not consume the existing work retry budget by rerunning
the same full investigation. It moves the work to a precise blocked or
dead-letter state and sends one private owner diagnosis when appropriate.

Background helper failures, compaction failures, and terminal-finalizer
failures do not invoke another model from the same error path. The runtime
emits the deterministic checkpoint, completed checks, exact blocker, and next
step.

## Persistence And Observability

SQLite schema v18 records role, profile, provider, protocol, model, capability
fingerprint, config fingerprint, phase, step, attempt, finish reason, HTTP
status, failure class, recovery action, request ID, token usage, cache usage,
and redacted diagnostics.

The trace hierarchy is:

`work item -> agent run -> turn -> model attempt/tool call`

Logs, queue inspection, and run transcripts must answer:

- whether the provider request was actually sent;
- which role/profile/model handled it;
- which step and attempt failed;
- whether the failure was retried;
- why the work stopped;
- what the next user or operator action is.

Raw thinking, encrypted reasoning payloads, API keys, request bodies containing
credentials, and private Lark message text copied from production are not
stored in fixtures, logs, or exported diagnostics.

## BDD Acceptance

### Scenario: Kimi thinking with tools

Given the `agent` role uses a Kimi `openai_chat` profile with provider-default
thinking, when a coding investigation exposes tools, then the outbound request
contains `tools` but not `tool_choice=required`, and the run can execute tools
and submit a decision.

### Scenario: Three protocol turn parity

Given OpenAI Chat, OpenAI Responses, and Anthropic mock providers each return
two parallel read-only tool calls, when the runtime decodes their responses,
then each produces equivalent local `Turn` values with call IDs, sibling
ordering, results, finish reason, usage, and request ID preserved.

### Scenario: Stream liveness

Given a provider stream opens but produces no valid event before the idle
timeout, when the step still has attempts remaining, then the attempt is
cancelled, classified as retryable timeout, and retried without consuming a
model turn.

### Scenario: Deterministic provider failure

Given the provider rejects a request with HTTP 400 because tool choice and
thinking are incompatible, when the runtime classifies the error, then the
work stops with a precise provider/protocol diagnosis and does not spend
general queue retries on the same request.

### Scenario: Stable prompt prefix

Given the same stable model profile and tool registry are rendered twice, when
only budgets, phase, timestamps, or recent failure state changed, then the
stable prefix hash remains identical and only the appended run-state reminder
differs.

### Scenario: Compaction keeps protocol units

Given an older assistant turn emitted two tool calls and one result is large
enough to trigger compaction, when the next provider request is built, then the
assistant call and both tool results remain a complete protocol unit or become
one deterministic checkpoint per call; no orphan result is sent.

### Scenario: Helper failure cannot recurse

Given context overflow triggers deterministic checkpointing and the `compactor`
model fails, when the main work continues, then the runtime preserves the
checkpoint, records the compactor failure, and does not call another model from
that same recovery path.

### Scenario: Regression fixture from real failure shape

Given a sanitized fixture shaped like failed work items `#5994`, `#6062`,
`#6070`, or `#6097`, when the harness runs it three times, then it reports
Pass@1, Pass^3, tool-call legality, terminal convergence, repeated-call count,
token/cache metrics, and final stop reason without real private chat text,
secrets, or raw thinking.

## TDD Test Locations

- `agent/runtime/model/*_test.go`: canonical request/turn/failure types,
  provider codec golden JSON/SSE fixtures, tool-choice and thinking encoding.
- `agent/runtime/openai_model_test.go`: compatibility shim and old adapter
  removal or delegation behavior.
- `agent/runtime/loop_test.go`: phase machine, per-step retries, no-progress,
  finalizer, compactor failure, repeated-call convergence, and helper
  non-recursion.
- `agent/config/config_test.go`: config v5 profile/role validation, v4/env
  migration, invalid role/profile failures, and secret-free YAML.
- `agent/storage/storage_test.go`: schema v18 run/step/attempt audit columns
  and migration.
- `agent/runtime/provider_doctor_test.go`: role/profile doctor requests,
  protocol capability diagnostics, and redacted failures.
- `integration_test/lark_agent/model_protocol_test.go`: protocol parity and
  request/turn audit.
- `integration_test/lark_agent/model_retry_convergence_test.go`: retryable
  vs non-retryable provider failures and terminal finalizer behavior.
- `integration_test/lark_agent/context_compaction_protocol_test.go`: stable
  prompt prefix, status bar truth, and protocol-safe compaction.
- `integration_test/lark_agent/harness_eval_test.go`: historical failure
  fixture metrics, Pass^3 stability, and no private data leakage.

## Documentation And Help

Update:

- `docs/configuration.md` for model profiles, role bindings, retry budgets,
  stream liveness, and credential storage;
- `docs/operations.md` for queue inspect diagnostics and provider failure
  meanings;
- `docs/install-macos.md` for migration from private `OPENAI_*` env to profile
  and Keychain storage;
- `README.md`, CLI `--help`, and Owner private `/help` for doctor and model
  profile commands.

Group-chat help remains private-state safe: group status/help requests only
redirect to private chat and do not reveal model, queue, task, approval, or
credential diagnostics.

## Installation And Migration

The installer migrates existing `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and
`OPENAI_MODEL` into the `primary` profile and Keychain only after validating
candidate config, Keychain readback, and `model doctor`. Unrelated private env
keys remain unchanged. If migration or doctor fails, config, env, Keychain, and
binary are restored from backup.

## Non-goals

- No automatic cross-provider failover, model racing, or cost-based model
  selection.
- No provider-hosted web search, remote MCP, Kimi OAuth, Ralph/D-Mail, or
  synchronous terminal UI.
- No tool execution before a model stream has produced a complete valid turn.
- No online self-modification, automatic prompt expansion, or automatic
  release of learned rules.
- No changes to Lark routing, group `@` conditions, delegated-reply identity,
  approval semantics, Workspace permissions, or send gates.

## Stop Conditions

The change is complete when long-term spec, config migration, provider codecs,
agent loop, storage audit, doctor, queue diagnostics, regression fixtures,
docs, help, full verification, independent review, commit, installation, and
`k3-256k` smoke evidence all agree with the behavior above.
