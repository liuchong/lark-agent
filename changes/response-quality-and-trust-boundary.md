# Response Quality and Trust Boundary

## Goal

Make Lark replies useful without allowing a chat participant to turn the
personal assistant into an environment-inspection or mutation proxy.

The accepted scope has four outcomes:

1. incidental app/bot traffic no longer crowds human conversation out of the
   model-visible context;
2. delegated replies perform and report bounded relevant work instead of only
   acknowledging or restating the request;
3. non-owner-triggered work can use only same-chat and workspace read evidence;
4. requests for out-of-workspace access or descriptive environment
   reconnaissance are refused before any evidence tool executes;
5. an approved reply preserves whether the original invocation addressed the
   assistant or the owner; approval never changes bot replies into delegated
   user-identity replies.

## Directly Applicable Rules

The implementation follows these repository rules verbatim:

- `任何方案必须先经用户确认再实施。`
- `所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按
  red -> green -> refactor 实现。`
- `每项逻辑变化必须有 integration_test/ 覆盖；没有相应集成测试时不得宣称完成。`
- `Workspace shell 必须由代码限制在配置的 Workspace 真实路径内；提示词不能代替
  路径、符号链接和子进程边界检查。`
- `飞书生产调用必须统一经过 internal/lark，只能调用官方公开 Go SDK
  github.com/larksuite/oapi-sdk-go/v3。`
- `当任务同时包含提交与安装/重启时，必须先完成验证并提交，用提交锁住已验证代码，
  再执行安装/重启。`

## Business Design

### Core Data

Every model run derives an ephemeral invocation scope from durable event data:

- `Owner`: whether `event.sender_id == configured owner.open_id`;
- `ReadOnly`: true for every non-owner sender;
- `ChatID`: the source chat that a non-owner-triggered run may inspect;
- `RequestGuard`: optional deterministic refusal reason for workspace escape,
  credential discovery, machine inventory, or other descriptive environment
  reconnaissance.

The scope is carried in the tool execution context. It is not model-authored,
is not persisted as a new schema field, and does not require a database
migration.

Tool definitions declare whether they have side effects, require the owner, or
accept a chat argument that must equal the source chat. The registry enforces
these declarations before the executor runs.

The runtime tracks successful non-terminal evidence calls for the current run.
A delegated work or coordination reply is useful only when it:

- has at least one successful relevant read receipt;
- says what bounded work was completed and gives a concrete finding or
  uncertainty;
- does not merely repeat the incoming request;
- does not invent a future personal or team commitment;
- cites production code for definite production-code claims.

### Decision Flow

1. Build the bounded current-message bundle.
2. Apply the deterministic request guard.
3. If guarded, return a concise refusal without calling Lark-context,
   workspace, code-index, or shell tools.
4. Otherwise expose only tools permitted by the invocation scope.
5. Resolve conversation context with direct reply/thread relations pinned.
   Unreferenced app/bot messages are excluded from adjacent context.
6. Execute bounded investigation. Tool-policy and argument-validation failures
   do not consume the successful investigation-call budget.
7. Validate `submit_decision`:
   - direct owner mentions may use `record` or `notify` when no useful
     sender-facing response exists;
   - delegated work/coordination replies require real read evidence and a
     substantive completed-work summary;
   - acknowledgement-only, high-restatement, and unapproved future commitment
     drafts are rejected for repair;
   - examples, tests, and documentation are supporting evidence, not proof of
     production implementation.
8. The existing reply controller applies identity, scope, owner-wait,
   withdrawal, approval, idempotency, and post-reply owner notification rules.

### Failure And Recovery

- A blocked tool returns a structured permission failure and never calls its
  executor.
- A guarded request produces a normal auditable reply decision; it does not
  enter retry solely because the request was refused.
- A low-quality terminal draft is returned to the model as a repairable tool
  error. Repeated no-progress handling and the existing finite turn budget
  still apply.
- Budget exhaustion produces a truthful partial result or explicit unknown.
  It never converts an empty acknowledgement into a valid reply.
- Existing work items and decisions require no migration and are never
  replayed automatically after installation.

### Permission And Silence Boundaries

- All local file paths remain inside the configured real workspace root.
- A non-owner sender cannot execute shell, search other Lark chats, or invoke
  any present or future owner-only/side-effect tool.
- A non-owner may read bounded same-chat context and workspace/code evidence
  needed for a business answer.
- Credential discovery, host inventory, user-home inspection, process/network
  inventory, and explicit reads outside the workspace receive a concise
  refusal.
- Non-business or probing content receives a refusal or ignore outcome; the
  model must not answer it using environment details.

### Configuration And Installation

The protection is mandatory and has no disable flag. Existing
`assistant.reply_scope` and `policy.reply_scope` remain independently
configurable; the actual installation keeps both at `all_groups`.

`assistant.reply_scope` controls only where the configured owner may directly
invoke the assistant bot. A non-owner private message or native assistant
mention is always silent and is rejected before queueing or model work.
`policy.reply_scope` independently controls where a non-owner may mention the
human owner and trigger a read-only delegated reply.

The default initial context is reduced and tool investigation budget is raised
only enough to support production-source verification after wasted validation
calls stop counting. The simple-request budget uses three model turns so the
runtime can search, read, and then submit a conclusion instead of ending after
the second tool batch. The installed config is updated to the same effective
values after code verification and commit.

### Fixtures

Regression fixtures use synthetic chat IDs, message IDs, names, paths, and
source code. The Backend Dev Team production trajectory is represented only by
an anonymized shape:

- human coordination request;
- nearby deployment-bot noise;
- one example-file source;
- acknowledgement/restatement draft with an unverified future commitment.

No Lark token, app secret, private message ID, real sender ID, or personal
credential is stored in the repository.

## BDD Acceptance

### Scenario: Same-chat read-only delegated investigation

Given another human mentions the owner with a business engineering request,
when the model reads same-chat context and production workspace code,
then the tools execute read-only, the reply briefly states completed
investigation and findings, and no mutation tool is available or executable.

### Scenario: Non-owner cannot invoke the assistant

Given a non-owner privately messages the assistant or natively mentions it in a
group, when intake and routing evaluate the event, then it is silently rejected
before queueing, model work, working reactions, or any reply.

### Scenario: Verifiable and false-premise quality probes

Given the owner asks one question whose answer can be independently verified
from production workspace source and one question containing a nonexistent
function or unsupported premise, when the installed assistant answers, then
the first answer matches the inspected source and the second explicitly rejects
or qualifies the premise without inventing files, functions, calls, or facts.

Given either probe has already produced citable workspace evidence, when the
model has enough evidence to answer every requested field or reaches its final
two turns, then it submits the evidence-backed answer or explicit unknown
instead of expanding into unrelated Lark history or exhausting the run and
retrying the whole investigation.

Given the resulting auto-mode reply has no approved draft to consume, when the
controller checks current and legacy approval keys, then absence is not an
identity error and the generated reply proceeds to the Lark send action.

### Scenario: Non-owner mutation request

Given a non-owner mentions the owner and asks the delegated workflow to modify,
delete, commit, deploy, or send through shell,
when the model attempts a side-effect tool,
then the registry rejects the call before execution and the final reply does
not claim the action happened.

### Scenario: Cross-chat or environment reconnaissance

Given a chat participant asks for another chat's messages, credentials, host
inventory, user-home contents, or an explicit path outside the workspace,
when the request is processed,
then no evidence tool executes and the sender receives a concise business-only
refusal.

### Scenario: Bot-noise context

Given an adjacent same-chat window contains a human target and unrelated app
deployment messages,
when context is compacted,
then the target and explicit relation messages remain while unreferenced
app/bot messages do not consume model-visible slots.

### Scenario: Empty acknowledgement

Given a delegated coordination request asks for investigation,
when the draft only says the owner was reminded or restates the request,
then the quality gate rejects it.

Given the run actually reads relevant context or code,
when the repaired draft concisely states the completed check, an initial
finding, and what was passed to the owner,
then the quality gate accepts it without requiring excessive detail.

### Scenario: Supporting evidence is not production evidence

Given a coding run reads only examples, tests, or documentation,
when it claims a definite production implementation,
then the verify gate rejects the reply and asks for a production source or an
explicit unknown.

### Scenario: Approval preserves assistant identity

Given an assistant group request or private owner request produces a useful
evidence-backed draft that is held for approval,
when the owner approves that exact draft and the daemon resumes it without
calling the model again,
then the reply is sent with bot identity, without the delegated robot prefix or
a post-reply owner notification.

### Scenario: Approval preserves delegated identity

Given a non-owner directly mentions the owner and its useful read-only reply is
held for approval,
when the owner approves that exact draft,
then the reply is sent with user identity, keeps the delegated robot prefix,
and the bot notifies the owner only after the external reply succeeds.

### Scenario: Legacy approval preserves identity and audit

Given an older exact-draft approval stores no relevance and uses the legacy
idempotency key,
and the associated work item stores its original decision relevance,
when the approved work resumes after upgrade,
then the daemon restores that relevance, consumes the legacy action exactly
once, sends with the original identity, and completes the legacy action with
the returned message ID.

Given both durable sources lack a recognized relevance,
when recovery starts,
then it fails before sending instead of guessing an identity.

### Scenario: Approval survives concurrent daemon writes

Given a daemon write transaction briefly overlaps an exact pending reply
approval,
when the operator runs `approval approve ACTION_ID` or
`approval reject ACTION_ID`,
then the command waits for the bounded SQLite busy interval and atomically
updates the action and work item after the daemon releases the lock,
without failing because a stale read snapshot cannot be upgraded to a writer.

### Scenario: Unauthorized commitment

Given a non-owner-triggered draft says that the owner or team will later
deliver, coordinate, or report back,
when no exact owner approval exists,
then the draft cannot be sent as an automatic reply.

### Scenario: Simple request retains a conclusion turn

Given a simple assistant request performs broad search in its first model turn
and narrowed production reads in its second,
when those reads complete,
then a third model turn remains for `submit_decision`.

Given an assistant request asks to inspect source code or a production/code
entry point inside the configured Workspace and provide code evidence,
when deterministic routing classifies the request,
then it enters `coding_question` and cannot be trapped in the simple-question
tool policy.

### Scenario: Production evidence converges immediately

Given a `coding_question` tool result contains a production source,
when the next model turn starts,
then only `submit_decision` is available and any attempted history, rules,
tests, search, or shell call is rejected without execution.

### Scenario: Missing reply confidence is repaired

Given the model submits a `reply` decision without an explicit
`reply_confidence`,
when the terminal decision is parsed,
then the model response is rejected and repaired inside the bounded loop
instead of becoming a zero-confidence approval.

## Test Locations

- `integration_test/lark_agent/response_quality_test.go`: end-to-end contracts
  for guarded requests, non-owner read-only execution, evidence-backed replies,
  and the anonymized regression trajectory.
- `integration_test/lark_agent/routing_classification_test.go`: exact live
  Workspace plus production-entry wording in assistant private chat and native
  group mention routes to the code-investigation lane.
- `agent/runtime/*_test.go`: terminal quality and production-source verification.
- `agent/tools/*_test.go`: owner-only, same-chat, and side-effect registry gates.
- `internal/lark/im_test.go`: app/bot context compaction.
- `agent/context/builder_test.go`: business-only, read-only, and concise-work
  prompt contract.

## Documentation And AI-Facing Records

Update `spec/behavior.md`, `docs/configuration.md`, `docs/operations.md`, CLI
help contract tests where visible behavior is described, and one reusable
`.agents/experience/` note about deriving tool authority from the durable
sender identity. No new CLI command is introduced, so command syntax help does
not gain a new flag.

## Non-Goals

- general content moderation or a universal natural-language business ontology;
- autonomous source-code editing from Lark;
- exposing host, Keychain, or cross-chat data after approval;
- changing Lark group scope semantics or replaying historical work;
- replacing the configured model provider solely to mask harness defects.

## Reserved Interfaces

The tool definition metadata may later support finer read classes, but this
change exposes no partially implemented policy mode or configuration flag.
Only the enforced owner/non-owner boundary is user-visible.

## Completion And Stop Conditions

Stop when focused red/green tests, all `integration_test/`, `go test -race
./...`, `go vet ./...`, lint, module-boundary checks, independent review, commit,
installation, and bounded international Lark validation in only “测试负责人的智能助手”
and “龙虾群🦞” have completed. Do not send validation messages to Backend Dev
Team or Example Group.
