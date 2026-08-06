# Semantic Owner Control And Proactive Delegated Replies

## Goal

Make the assistant useful without making it reckless:

- understand contextual owner-private expressions such as "确认", "发吧",
  "不要发", "看看最近任务", and "不用继续了" as typed control commands only
  when the surrounding assistant notice and durable candidates make the target
  unique;
- preserve the current assistant application's bounded private-chat messages
  so short follow-ups are not interpreted without their antecedent;
- send verified low-risk delegated replies directly instead of routing nearly
  every draft to owner approval;
- provide the model with a bounded five-level workspace/project overview,
  bounded read-only local Git history, and durable owner-curated memory;
- cancel or suppress work that the owner has already handled before confidence
  or approval is considered.

The installed production configuration uses all-group assistant and delegated
reply scope, a three-minute owner wait, and a delegated reply confidence floor
of `0.70`.

## Confirmed Hard Rules

The implementation is governed by these repository rules:

> "所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按
> red -> green -> refactor 实现。"

> "每项逻辑变化必须有 `integration_test/` 覆盖；没有相应集成测试时不得宣称完成。"

> "飞书生产调用必须统一经过 `internal/lark`，只能调用官方公开 Go SDK
> `github.com/larksuite/oapi-sdk-go/v3`。"

> "Workspace shell 必须由代码限制在配置的 Workspace 真实路径内；提示词不能代替
> 路径、符号链接和子进程边界检查。"

The parent workspace additionally requires a clean status/diff/test review
before commit, and requires verification plus commit before installation or
restart.

## Business Design

### Canonical Command Catalog

`agent/control` owns one typed command catalog. Each entry contains:

- canonical command name and accepted slash aliases;
- localized usage and purpose;
- whether it is read-only or mutating;
- required typed arguments;
- whether semantic execution requires one exact durable work/action candidate;
- positive and negative semantic examples.

The explicit slash parser, `/help`, detailed help, and the semantic resolver
all consume this catalog. Adding a command without adding it to the catalog is
a test failure. Explicit slash commands remain deterministic and never call a
model.

### Semantic Command Resolution

Semantic control is available only when all of these facts hold:

1. the sender is the configured owner;
2. the chat is the assistant's configured private chat;
3. the message is not an explicit slash command;
4. bounded adjacent context includes the current message;
5. the resolver receives the canonical catalog and typed currently eligible
   tasks/approvals from storage.

The resolver returns one of:

- `not_command`: continue through the ordinary owner-question model path;
- `command`: execute one catalog command;
- `ambiguous`: reply with a concise clarification and exact candidate IDs.

Read-only semantic commands require confidence at least `0.85`. Mutating
commands require confidence at least `0.95`, one exact eligible candidate, and
arguments that validate through the ordinary typed parser/handler. The model
cannot invent work or action IDs: output IDs must occur in the supplied
candidate set. "确认一下这个问题是什么" is an ordinary question. A bare
"确认" following one exact approval notice approves that action; the same text
with multiple unanchored approvals is ambiguous. All semantic mutations use
the same journal and storage transition as their slash equivalents. A
command-shaped model result below the read-only confidence floor falls through
to the ordinary question path instead of asking a control-plane clarification.
High-confidence ambiguity uses deterministic candidate-ID text rather than
model-authored prose.

### Trusted Runtime Policy

Every ordinary answer bundle contains a non-secret snapshot of the validated
active runtime policy: mode, assistant/delegated/private scopes, owner wait,
owner-answer semantic threshold, delegated direct-send threshold, retry
interval, and investigation-progress mode. The snapshot is authoritative for
questions about the assistant itself. Workspace rules remain untrusted
project-investigation input and cannot redefine or stand in for runtime
configuration.

The prompt names the two confidence values by behavior. In particular,
`owner_reply_confidence_min: 0.85` is the minimum confidence for deciding
whether the owner already answered a pending message, while
`reply_confidence_min: 0.70` is the low-risk delegated draft's automatic-send
floor. The bounded bundle preserves this snapshot during context compaction.

### Trusted Private Context

`MessageContextRequest` carries an explicit app-message retention policy.
Ordinary group/delegated context keeps the existing behavior of discarding
unpinned app noise. Owner-assistant private context keeps bounded messages sent
by the current assistant app. The policy is selected by the caller from
deterministic identity/routing facts, not inferred by the model.

Owner notices must say whether work is complete, pending approval, or still
running. A notice that needs owner action includes the exact action ID and
valid command. A completed automatic reply says no action is required and does
not ask the owner to "确认是否继续". A notice is not allowed to describe an
unsent draft as already sent.

### Delegated Reply Decision

The final send gate applies deterministic terminal facts in this order:

1. paused/identity/forbidden checks;
2. blocked chat/user and configured scope;
3. withdrawn-message and owner-semantic-reply checks;
4. risk and exact-action requirements;
5. confidence and approval mode.

Therefore an owner-handled or withdrawn message is cancelled even if the draft
confidence is low. A verified, low-risk delegated reply at confidence `0.70`
or above sends automatically after the three-minute semantic wait and final
thread-state recheck. Medium/high risk, personal commitments, destructive
actions, or insufficient evidence still require approval or are blocked.
The runtime performs the same side-effect-free thread-state preflight before
announcing that an automatic reply will be sent, then repeats it immediately
before the external send.

Pending approvals are rechecked before approval execution. If the owner has
answered, the source message was withdrawn, or the discussion resolved the
request, the approval becomes cancelled with an audit reason and no sender
message. The same recheck runs immediately before every external send.

### Workspace Project Context And Local Git

The initial workspace tree is bounded to:

- depth 5;
- 600 total entries;
- 80 entries per directory;
- 16 KiB serialized directory/project prompt budget.

A project catalog identifies bounded Git repository and language-manifest
roots and their relative paths. A Git repository with no recognized language
manifest remains present. The catalog is prompt-prioritized over raw leaf
entries. Paths are always relative to the configured real workspace root.

The model receives an `inspect_git_history` tool. It accepts a workspace-relative
repository path and optional bounded `max_commits`, reads only the local
repository, returns at most 20 commits and 8 KiB, and performs no fetch,
checkout, mutation, hook, or network operation. Repository real paths and Git
metadata must remain inside the workspace boundary. The child process removes
all inherited `GIT_*` redirect variables before applying its fixed read-only
environment. Non-owner delegated runs may use it because it is read-only;
shell write and external side-effect permissions remain denied.

### Durable Memory And Feedback

SQLite schema version 15 adds:

- `memory_entries(id, kind, scope, content, source_work_item_id,
  source_message_id, confidence, created_at, updated_at, deleted_at)`;
- `memory_feedback(id, memory_entry_id, verdict, note, source_message_id,
  created_at)`.

`kind` is one of `fact`, `preference`, `project`, or `response_feedback`.
`scope` is a workspace-relative project key or `global`. Raw chat transcripts,
credentials, model chain-of-thought, and unverified model guesses are not
memory entries. Owner-authored corrections and explicit `/memory add` commands
may create entries. Automatic extraction may only create a low-confidence
candidate; it becomes prompt-visible only after owner confirmation. Common
provider token prefixes, cloud access keys, private-key material, and
password/secret assignments are rejected at the storage boundary.

Retrieval is bounded by scope, query terms, recency, confidence, count, and
serialized bytes. Deleted entries are tombstoned for audit and excluded from
prompts. `/memory list`, `/memory add`, `/memory delete`, and `/memory feedback`
use the same typed owner-private command catalog and storage.

### Failure And Recovery

- Missing or truncated context never guesses a semantic mutation; it asks one
  precise clarification.
- A semantic resolver/model failure falls through to an ordinary owner
  question only when the text is not an explicit control command. It never
  converts an uncertain mutation into approval.
- A stale approval is cancelled and audited; it is never replayed.
- Old historical work discovered during migration is audited or discarded
  according to terminal facts and does not emit sender-facing messages.
- Context, project-tree, Git, and memory output is independently byte-bounded;
  compaction retains the current message, its exact actionable assistant
  antecedent, command catalog, project catalog, and verified evidence before
  lower-priority history.

## Given / When / Then

### Owner Private Context Regression

Given the current assistant sent one actionable private notice and the owner
then sends "确认", when adjacent context is built, then both messages are
present in chronological order and the selection is not marked incomplete
merely because the preceding message has sender type `app`.

Given an unpinned app message in an ordinary group context, when context is
compacted, then that app message is still discarded as noise.

### Semantic Commands

Given one pending approval and the immediately preceding assistant notice names
its exact action ID, when the owner says "确认", then the typed approve command
for that action executes once without calling the general answer model.

Given two pending approvals and no reply/adjacent notice that uniquely selects
one, when the owner says "确认", then no mutation executes and the assistant
asks which exact action ID was intended.

Given the owner asks "确认一下这个修复是否上线了", when semantic resolution
runs, then it returns `not_command` and the ordinary business-question path
continues.

Given a model emits a command-shaped result below confidence `0.85`, when
semantic resolution runs, then no control clarification or mutation occurs and
the ordinary business-question path continues.

Given the owner asks an ordinary question containing "确认" about current
automatic-send behavior, when the question reaches the answer model, then its
bundle includes the exact configured `0.85` owner-answer threshold and `0.70`
direct-send threshold with distinct meanings, and workspace rules cannot be
used to infer different runtime values.

Given a new command is added to the catalog, when parser/help/prompt contract
tests run, then its aliases, usage, and semantic description appear from that
same catalog without a second hard-coded list.

### Proactive Reply

Given a delegated request has verified evidence, low risk, confidence `0.70`,
and no owner answer after three minutes, when the final send gate runs, then it
sends automatically and records the reply and owner notice.

Given the same draft but the owner already answered or the source was
withdrawn, when the final gate runs, then it cancels before confidence or
approval and sends neither an automatic-send notice nor a sender reply.

Given an approval is waiting and the owner later resolves the discussion, when
approval is requested or executed, then the approval is cancelled as stale and
no historical draft is sent.

### Project Evidence And Memory

Given a nested repository within five workspace levels, when the initial
bundle is built, then its relative project root is present even when leaf
entries are truncated.

Given a repository path that escapes through `..` or a symlink, when
`inspect_git_history` runs, then it is rejected before invoking Git.

Given inherited `GIT_DIR` or `GIT_WORK_TREE` points outside the workspace, when
`inspect_git_history` runs for a validated repository, then the inherited
redirect is ignored and only the validated repository is read.

Given an owner-confirmed memory entry, when a related later request is built,
then bounded retrieval includes it after restart. Given a deleted or
unconfirmed entry, then it is absent from the prompt.

Given the owner corrects a prior answer or states one stable preference in the
assistant private chat without explicitly invoking `/memory`, when semantic
control classifies the message, then it may persist one bounded candidate with
that exact owner message as source. The candidate remains absent from model
context until the owner confirms it; credential-like content and duplicate
candidates are discarded.

Given explicit memory content contains a common provider token, cloud access
key, or password/secret assignment, when either bot or local memory commands
validate it, then no plaintext memory row is written.

## Test Locations

- `internal/lark/im_test.go`: trusted app context and group noise regression.
- `agent/control/*_test.go`: catalog/parser/help/semantic validation.
- `agent/app/app_test.go`: semantic command dispatch and stale approval checks.
- `agent/policy/policy_test.go`: final-gate ordering and `0.70` low-risk send.
- `agent/context/builder_test.go`: five-level project catalog and prompt budget.
- `agent/tools/*_test.go`: local Git workspace confinement and output bounds.
- `agent/storage/*_test.go`: schema 15 memory persistence and feedback.
- `integration_test/lark_agent/`: owner-private contextual command,
  proactive-reply, project/Git evidence, memory restart, authorization, and
  old-work non-replay scenarios.

Fixtures use temporary workspaces, local temporary Git repositories, fake
official-SDK callers, deterministic model outputs, and temporary SQLite
databases. They contain no real open IDs, tokens, message text, or absolute
personal paths.

## Documentation And Installation

Update `README.md`, `docs/operations.md`, generated command help, `/help`, and
the sample/installer configuration. Production installation keeps
`reply_scope: all_groups`, `private_reply_scope: all`, `owner_wait: 3m`, and
sets `reply_confidence_min: 0.70`. Installation happens only after verification,
review, commit, and push.

## Non-goals

- No arbitrary natural-language shell or filesystem command execution.
- No semantic control for non-owners or in groups.
- No network Git operations or GitHub replacement through local Git history.
- No raw transcript archive or autonomous profile-building from private chats.
- No reply or replay of historical stale work during migration/cleanup.
- No bypass of approval for destructive actions, commitments, or uncertain
  external side effects.
