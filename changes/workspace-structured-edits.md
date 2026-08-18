# Workspace Structured Edits

## Confirmed Scope

Keep the existing Lark assistant. Add structured workspace file mutation, and
bring read, search, list, and shell receipts up to the same production path.

The user confirmed this behavior: investigation stays read-only by default;
`edit_workspace` and `write_workspace` appear only when the target message
explicitly asks to modify, fix, or implement; writes stay owner-only, inside
the configured workspace, and must not replace `submit_decision` or use
`shell` as the primary way to change files.

## Directly Applicable Hard Rules

- `任何方案必须先经用户确认再实施。`
- `所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按
  red -> green -> refactor 实现。`
- `每项逻辑变化必须有 integration_test/ 覆盖；没有相应集成测试时不得宣称完成。`
- `Workspace shell 必须由代码限制在配置的 Workspace 真实路径内；提示词不能代替
  路径、符号链接和子进程边界检查。`
- Public repository artifacts must not contain real people, chat/message/user
  identifiers, private repository names, private filesystem paths, secrets, or
  copied production conversation text.

## Non-goals

- Do not turn this assistant into a standalone programming IDE.
- Do not grant write tools to non-owner senders.
- Do not let a coding conclusion cite a mutated file without a later
  digest-backed `read_workspace`.
- Do not make `shell` the primary file-edit path.
- Do not add a second identity or approval model; reuse the existing owner
  scope and shell-approval store.
- Do not mix this change with unrelated remote-PR or model-failure work.

## Business Design

### Core model

Workspace files remain bounded by the configured real root, exclude list, and
symlink checks. Structured mutation is a first-class tool, not a shell script.

Write tools are owner-only and `WorkspaceWriteOnly`. The loop sets
`WorkspaceWriteAllowed` only when the target message explicitly asks to
modify, fix, or implement. Locating an implementation file is not a write
request.

### State and decision rules

- `edit_workspace`: replace exact unique `old_text` with `new_text` in an
  existing file. One call may include several replacements; every replacement
  is matched against the original file; replacements must not overlap; the
  file is unchanged on any failure.
- `write_workspace`: create a file or overwrite the whole file. Parent
  directories may be created inside the workspace. Use this only for a new
  file or a full rewrite.
- Same-file mutations are serialized.
- After a successful mutation, previous `workspace_file` digests for that
  path are no longer citable. The model must `read_workspace` again.
- Plan-mode coding work still denies production edits and write shell
  commands. When `shell_approval` is enabled, mutations use the same
  one-time exact-argument approval store as shell.

### Read, search, list, and shell completeness

- `read_workspace` accepts optional 1-based `offset` and `limit` line range.
  The source digest is the whole file. Returned content stays within
  `max_bytes`.
- `search_workspace` accepts optional `glob`, `literal`, `regex`, and
  `context_lines`. Default matching stays case-insensitive phrase, then
  all whitespace-separated terms in one file.
- `list_workspace` accepts optional `glob` to list matching files without
  leaving the workspace.
- Shell stdout/stderr that exceeds the configured output bound is stored as a
  workspace-local file under `.local/lark-agent/runtime/shell-output/`. The
  tool result keeps a short preview plus path, digest, and byte count.
- If the model finish reason is truncated (`truncated` or `length`) and the
  turn includes tool calls, those calls are incomplete: the runtime does not
  execute them, returns a failure receipt per call id, and asks the model to
  retry with complete calls.

### Failure and fallback

- Non-unique or overlapping `old_text` fails with a validation error; bytes
  on disk do not change.
- Paths outside the workspace, excluded secret/credential/VCS paths, and
  symlink escapes fail the same way as reads.
- Truncated model output must not become a partial write.

## BDD

Given the owner asks to fix or implement a workspace file
When the run starts
Then `edit_workspace` and `write_workspace` are visible to the owner
And a successful unique replacement changes the file digest
And a later `read_workspace` of that path is required before citing it

Given a coding question only asks what the code currently does
When the run starts
Then write tools are absent from the model catalog
And an attempted `edit_workspace` call is denied
And the file is unchanged

Given a non-owner sender
When the run starts
Then write tools are hidden and denied

Given one `edit_workspace` call with overlapping or repeated `old_text`
When it executes
Then it fails
And the file bytes are unchanged

Given `write_workspace` targets a missing path inside the workspace
When it executes
Then parent directories are created and the file is written

Given two mutations target the same relative path
When they run in one turn
Then they are applied one after another
And neither write is dropped

Given the model finishes with truncated tool calls
When the loop handles that turn
Then no tool executor runs
And each call id receives a failure receipt

Given `read_workspace` is called with `offset` and `limit`
When the file is larger than the default byte cap
Then the result contains only that line range
And the source digest is the whole file

Given `search_workspace` is called with glob, regex or literal, and context
When matches exist inside the workspace
Then results stay inside the requested tree and include the requested context

Given shell output exceeds the configured byte bound
When the command finishes
Then the model-visible result is a preview plus a workspace-local spill file
with digest and size
And no secret files outside the workspace are written
