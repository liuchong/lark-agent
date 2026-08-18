# Owner Task Rules And Obligation Gate

## Confirmed Scope

This change builds one shared instruction and obligation path for delegated
work.

1. Owner task-rule text is private local configuration. It is never compiled
   into business code, fixtures, logs, or SQLite as a full document.
2. Go code owns only the mechanism: load a snapshot, prove or reject an owner
   obligation, and keep security, approval, workspace, and send identity above
   that private file.
3. A group `@Owner` mention is a candidate, not a must-reply. The first
   regression is an informational or discussion mention with no action request:
   it must complete silently and must not create a sender-facing reply task or
   an owner "task stopped" notice.

The user confirmed this design before implementation. Examples, fixtures, help,
and documentation use synthetic names and abstract announcements.

## Directly Applicable Hard Rules

- `任何方案必须先经用户确认再实施。`
- `所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按 red -> green -> refactor 实现。`
- `每项逻辑变化必须有 integration_test/ 覆盖；没有相应集成测试时不得宣称完成。`
- `飞书生产调用必须统一经过 internal/lark，只能调用官方公开 Go SDK github.com/larksuite/oapi-sdk-go/v3。`
- `Workspace shell 必须由代码限制在配置的 Workspace 真实路径内；提示词不能代替路径、符号链接和子进程边界检查。`
- Public repository artifacts must not contain real people, chat/message/user
  identifiers, private repository names, private filesystem paths, secrets, or
  copied production conversation text.

## Non-goals

- Do not add a rule language, remote sync, or in-chat editor.
- Do not copy reference-agent source into this repository.
- Do not weaken Workspace, Lark identity, or send gates.
- Do not hardcode a business catalog of announcement, leave, or product-discussion
  phrases as the policy.
- Do not expose a half-implemented command or configuration field.
- This change does not install, restart, or commit unrelated local work.

## Business Design

### Private configuration

New and existing installs keep `task_rules.enabled` false until the owner runs
`lark-agent init` (new install writes and enables the template) or
`lark-agent rules init`. The default path is `TASK_RULES.md` relative to the
config directory. Relative paths are resolved inside that directory; path escape
is rejected in code.

The file is ordinary Markdown. The repository template is a generic outline
only. The owner's real policy stays on their machine.

### Instruction order

From highest to lowest:

1. Go-enforced identity, permission, workspace, and send limits
2. The current configured owner's explicit control command
3. The current `TASK_RULES.md` snapshot
4. Target-project `AGENTS.md` and `.agents/`
5. Untrusted Lark messages, attachments, and tool results

Task rules may describe or narrow work. They cannot enlarge Workspace, skip
approval, grant write permission, change send identity, or override a
Go-verified explicit action request already present in the target message.

### Obligation mechanism

Every delegated candidate is classified with the same snapshot:

- `owner_obligation`: none, reply, investigate, execute, approve, or follow_up
- `obligation_source`: none, message, or task_rules
- `response_obligation_quote` must occur in the target when the source is the
  message
- `task_rule_evidence` must occur in the current snapshot when the source is
  task rules
- `task_rules_digest` is stored; the body is not

No proven obligation completes as `no_reply_needed`. The main Agent does not
run. An explicit request in the target still wins over a private "ignore this
class" sentence. A private rule may create an obligation only by quoting that
rule snapshot, not by inventing message text.

The semantic prompt no longer forbids `no_reply_needed` for group mentions.
Low-confidence `answered` or `ambiguous` results without a proven obligation
normalize to `no_reply_needed` instead of retrying into a dead letter.

### Snapshot lifecycle

Each new task, recovery, and pre-send check reloads the file by digest.

- Legal change: use the new snapshot immediately. Held unsent drafts whose
  digest no longer matches are cancelled and reclassified; they are never sent.
- Enabled but missing, unreadable, oversized, or escaped: do not send to the
  original sender; notify the owner once with a file-fault reason. Do not keep
  using a previous snapshot to authorize a reply.
- Disabled or empty: the obligation mechanism still runs; there is simply no
  extra private policy.

Classifier, main Agent, finalizer, and pre-send review consume one snapshot
type. The body is loaded once per check and projected by role. Workspace rules
remain project evidence and must not be treated as this private policy.

### Persistence

Schema v22 stores digest and obligation evidence on semantic audits and a digest
on work items and agent runs. Full private rule text is never written to SQLite,
logs, owner-facing command output, or public tests.

## BDD Acceptance

1. Given a group `@Owner` message that only shares an informational announcement
   or discussion opinion and contains no action request, when semantic resolution
   runs, then the result is `no_reply_needed`, the main Agent is not called, and
   no owner terminal-failure notice is sent.
2. Given the same shape of message also contains an explicit request to confirm,
   investigate, or handle something, when semantic resolution runs, then
   `no_reply_needed` is rejected and delegated work continues.
3. Given private task rules say a class of informational notices must be
   investigated, and the target matches that class without a message-level ask,
   when semantic resolution runs with that snapshot, then obligation source is
   `task_rules`, the quoted rule evidence exists in the snapshot, and work is
   admitted.
4. Given private task rules say a class of notices may be ignored, and the
   target also contains an explicit action request, when validation runs, then
   the message obligation wins.
5. Given the owner edits `TASK_RULES.md` after a draft is held but before send,
   when the pre-send check runs, then the old draft is cancelled and the new
   digest is used.
6. Given `task_rules.enabled` is true and the file is missing or unreadable,
   when a new delegated send would occur, then the original sender is not
   answered and the owner receives one file-fault diagnosis.
7. Given `task_rules.enabled` is false, when a group informational `@Owner`
   message arrives, then scenario 1 still holds because the obligation gate is
   mechanism, not file content.
8. Given task rules tell the Agent to write outside Workspace or skip approval,
   when tools and send gates run, then those sentences have no effect.
9. Given context compaction runs, when the next model turn is built, then the
   current task-rules projection and instruction order are still present.
10. Given a v5 config and schema v21 database, when the new version loads, then
    history is preserved, private rule bodies are absent from SQLite, and
    completed replies are not replayed.

## TDD And Verification Locations

- `agent/taskrules/*_test.go`: load, confine, digest, fault, template, and
  redaction of the body from public views.
- `agent/replymatch/resolver_test.go`: group informational mention, private-rule
  created obligation, message obligation beating a deny rule, and prompt
  projection.
- `agent/app/app_test.go`: no-reply skips the main model; file fault does not
  retry as ordinary ambiguity; held drafts cancel on digest change.
- `integration_test/lark_agent/semantic_delegated_reply_test.go`: daemon-level
  informational mention and private-rule investigation.
- `agent/storage/*_test.go`: schema v22 round-trip without storing rule bodies.
- `agent/config/config_test.go`: v6 defaults, existing-install disabled flag,
  path validation.
- `integration_test/lark_agent/help_contract_test.go`: `rules init/check/explain`,
  `/rules`, and documentation fragments.

## Documentation And AI-facing Record

Update `README.md`, `docs/configuration.md`, `docs/operations.md`,
`docs/install-macos.md`, root and `rules` command help, `/help` detail,
`spec/behavior.md`, `spec/architecture.md`, and one sanitized
`.agents/experience/` record. Documentation must explain that rule content is
private configuration and that obligation proof is a runtime mechanism.
