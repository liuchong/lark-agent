# Contextual Delegated Investigation

## Goal

Make delegated replies understand the actual handoff subject from bounded Lark
conversation context, including relevant images, and complete real read-only
investigation instead of reacting literally to an ambiguous final sentence.

The regression shape is:

- people discuss a production sample-event failure;
- an image contains `1408 SampleEventDisabled`;
- the target says `@Owner please take a look, my computer disconnected`;
- later messages clarify that production is not online;
- the assistant must investigate message editing, not the sender's network.

## Directly Applicable Rules

- `任何方案必须先经用户确认再实施。`
- `所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按
  red -> green -> refactor 实现。`
- `每项逻辑变化必须有 integration_test/ 覆盖；没有相应集成测试时不得宣称完成。`
- `飞书生产调用必须统一经过 internal/lark，只能调用官方公开 Go SDK
  github.com/larksuite/oapi-sdk-go/v3。`
- `Workspace shell 必须由代码限制在配置的 Workspace 真实路径内；提示词不能代替
  路径、符号链接和子进程边界检查。`
- `当任务同时包含提交与安装/重启时，必须先完成验证并提交，用提交锁住已验证代码，
  再执行安装/重启。`

## Business Design

### Context Snapshot

One semantic context snapshot is created after the owner wait window and reused
by owner-answer resolution, contextual task classification, and the main Agent.
It contains:

- at most 20 same-chat messages before the target, bounded to three minutes;
- the exact target;
- same-chat messages after the target through the semantic cutoff;
- explicitly related reply, root, and thread messages;
- sender open ID, sender display name, message type, text, timestamps, and
  bounded attachment descriptors;
- an incomplete reason when any required relation or attachment is unreadable.

The snapshot is ordered chronologically and has one content digest. Raw image
bytes are never persisted. A durable investigation stores the normalized
messages, cutoff, digest, and classification. A restart restores that exact
text/metadata snapshot, marks discarded image bytes explicitly unavailable,
and skips initial reclassification; the fresh final owner-handled check still
runs before any sender-facing reply.

### Attachment Boundary

`internal/lark` parses image keys and downloads message images through the
official public Go SDK. The runtime accepts only JPEG, PNG, GIF, and WebP image
content. It reads images serially, at most two per investigation, at most
1 MiB per image and 2 MiB total. An over-limit, unsupported, missing, or
unauthorized image remains a typed unreadable attachment and never becomes
empty evidence.

Image bytes live only for one model request and are released immediately after
serialization. The OpenAI-compatible adapter forwards Eino multimodal user
content as text and `image_url` parts. A configured vision model takes
precedence; otherwise the main model is used only after a capability probe
succeeds. Provider rejection marks the attachment unreadable and the Agent must
use textual evidence or state the exact unknown.

Encoded multimodal parts count toward model-visible request bytes. They are
present on the first model turn only; subsequent turns replace each data URL
with an explicit expiration marker so automatic compaction and urgency prompts
reflect the actual request rather than silently ignoring image payloads.

### Contextual Task Classification

The semantic resolver returns, in addition to answered/unanswered:

- `task_summary`: the concrete subject inferred from the bounded snapshot;
- `task_class`: `simple`, `investigation`, or `coding`;
- `classification_confidence`;
- `requires_progress`: true only when durable follow-up work is needed.

Group owner mentions cannot become `no_reply_needed`. A high-confidence
`coding` result receives the coding investigation budget and read-only
workspace tools even when the literal target text contains no code keyword.
Low-confidence classification does not widen tools or fabricate a subject; it
retries semantic resolution or produces a bounded evidence-limited reply.

### Durable Investigation

One `delegated_investigations` row is keyed by work item:

- `task_summary`;
- `task_class`;
- `context_cutoff`;
- `context_digest`;
- normalized `context_messages` without ephemeral image bytes;
- `status`: `pending_progress`, `investigating`, `finalizing`, `completed`, or
  `blocked`;
- `progress_action_id` and `final_action_id`;
- `last_error`, `created_at`, and `updated_at`.

For high-confidence work classified as investigation or coding:

1. Persist `pending_progress`.
2. Durably notify the named owner of the exact investigation subject.
3. Persist and send one same-thread progress reply with a stable action key.
4. Move to `investigating` and run bounded read-only Agent work.
5. Recheck post-cutoff owner handling before finalization.
6. Persist and send one evidence-backed final reply, owner-handled closure, or
   explicit blocked result.
7. Mark the investigation terminal only after the final action is known.

A progress reply may promise a later result only because the durable
investigation requires a terminal closure. No other delegated response may
make a future commitment.

Completed progress and final actions are never duplicated. A read/model crash
re-enters `investigating`. A known send failure retries the exact action. An
uncertain external send is never replayed and follows the existing owner
reconciliation contract. Work older than the existing stale-work boundary is
discarded or summarized privately and is never newly replied to.

### Evidence Quality

A successful read receipt must add non-empty evidence relevant to the task
summary. Re-reading the same context digest, receiving an empty image, or
reading unrelated files does not increment completed-work evidence.

The final delegated reply states:

- what same-chat or production source was checked;
- the initial finding or exact unknown;
- what information was sent to the named owner.

Unsupported definite claims, acknowledgement-only text, and unbacked future
commitments are rejected before any send.

### Permission And Silence Boundaries

- Non-owner delegated work remains read-only and same-chat/workspace bounded.
- Shell, mutation, deployment, cross-chat search, credential access, and paths
  outside the configured real workspace remain unavailable.
- Non-owner private messages to the bot and non-owner native bot mentions
  remain silent before queueing.
- Owner-authored messages that do not invoke the assistant are never answered
  by the delegated path.

### Configuration And Installation

New optional configuration:

- `agent.vision_model`: image-capable model name;
- `agent.max_context_images`: default `2`, maximum `2`;
- `agent.max_context_image_bytes`: default `1048576`, maximum `1048576`;
- `agent.max_context_image_total_bytes`: default `2097152`, maximum `2097152`;
- `policy.investigation_progress`: `enabled` or `disabled`, default `enabled`.

The installed international Lark instance uses `enabled` progress, all-group
assistant scope, all-group delegated scope, and a model proven by a live image
probe. Secrets remain in Keychain.

### Commands And Documentation

`/tasks` and `/task ID` expose investigation status, subject, evidence state,
last error, and the exact next owner command. `/help`, Cobra help,
configuration, installation, and operations documentation use the same terms.

## BDD Acceptance

### Scenario: Contextual handoff selects the preceding production issue

Given a bounded group window discusses message editing, contains an image with
`1408 SampleEventDisabled`, and the target says the sender's computer disconnected,
when delegated semantic resolution and the main Agent run,
then the task summary is the production sample-event failure, the run receives
coding tools and budget, and no reply treats the sender's network as the issue.

### Scenario: Later clarification remains evidence

Given people clarify after the target that production is not deployed,
when the owner wait window ends,
then the clarification is included through the fixed semantic cutoff and the
Agent uses it without claiming it existed before the target.

### Scenario: Image is useful or explicitly unreadable

Given a relevant Lark image is within the bounded limits,
when the configured provider accepts multimodal input,
then its contents are available as evidence without persistence.

Given the image is too large, unavailable, unauthorized, or rejected by the
provider,
when the Agent answers,
then it states that the image could not be verified and does not invent its
contents.

### Scenario: Durable progress always closes

Given high-confidence contextual work requires investigation,
when the semantic gate admits it,
then one owner notice and one progress reply are durably recorded before Agent
work and the investigation eventually sends one final, owner-handled, or
blocked closure.

Given the daemon restarts after progress,
when recovery runs,
then completed progress is not duplicated, the original normalized context
snapshot is restored without image bytes, initial classification is not
repeated against a new cutoff, and read-only investigation resumes.

### Scenario: Reply relations remain available

Given an ambiguous target replies to a root or thread message outside the
adjacent chat page,
when semantic context is selected,
then the readable relation is included in the shared snapshot; if it cannot be
read, resolution is incomplete and no antecedent is guessed.

### Scenario: Empty repeated reads do not count

Given the model repeatedly requests the same context or receives an image with
no readable content,
when response quality is checked,
then those calls do not satisfy completed-work evidence and a pretend
investigation result is rejected.

### Scenario: Verifiable basic and project answers

Given the assistant receives an independently verifiable arithmetic question,
when it replies,
then the result matches direct calculation.

Given the contextual task asks why production returns
`1408 SampleEventDisabled`,
when the configured workspace contains the current backend source,
then the answer identifies the old direct-disabled HTTP path, the current RPC
forwarding path, and the need to verify the deployed version from production
evidence.

### Scenario: False premise remains unknown

Given a question names a nonexistent production symbol, configuration, commit,
or deployment fact,
when bounded searches find no authoritative source,
then the answer says it was not found and does not invent a path or conclusion.

### Scenario: Security and silence remain unchanged

Given a non-owner asks the delegated workflow to mutate code, deploy, inspect
another chat, read credentials, or escape the workspace,
when tool authorization runs,
then execution is denied before the provider and the reply does not claim the
action occurred.

Given a non-owner directly invokes the bot,
when intake runs,
then no work item, model call, reaction, or reply is created.

## Non-goals

- No general OCR service or unrestricted file-analysis subsystem is added.
- No image or conversation raw content is persisted beyond existing bounded
  typed event data and digests.
- No old delegated target is automatically replayed for live acceptance.
- No delegated sender gains write, deployment, shell, cross-chat, or
  out-of-workspace authority.
- No progress reply is exposed for work that cannot be durably resumed and
  closed.
