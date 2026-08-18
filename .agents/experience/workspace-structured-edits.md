# Structured workspace edits stay on the production tool path

Investigation remains read-only by default. `edit_workspace` and
`write_workspace` appear only when the target message explicitly asks to
modify, fix, or implement, and only for the configured owner. Locating an
implementation file is not a write request.

- Keep structured mutation as the primary file-change path. Do not use `shell`
  to paper over missing edit tools.
- One `edit_workspace` call matches every replacement against the original
  file. Overlapping or non-unique `old_text` must fail without writing.
- After a successful mutation, drop previous `workspace_file` digests for that
  path. The next citable evidence must come from a new `read_workspace`.
- Truncated model output (`truncated` or provider `length`) with tool calls
  must not execute those calls. Return a failure receipt per call id.
- Oversized shell streams belong in `.local/lark-agent/runtime/shell-output/`
  inside the workspace, with a preview plus digest in the tool result.
