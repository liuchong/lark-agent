# Smart command GitHub boundary

A smart command is the same agent main loop, without a second Lark WebSocket.
`github notify` stays an ordinary HTTP-only send. `github run` is built-in GitHub
support, not a nickname for the loop.

Workflow jobs that need secrets checkout the trusted default branch Action
implementation (`ref: github.event.repository.default_branch`,
`persist-credentials: false`). They must not use `pull_request_target`,
download artifacts, or execute PR-head code.

Slash commands are not one-per-example-workflow. `@lark-agent` wakes the
comment path; `/review`, `/title`, and `/check` only union extra write names
and append a contract prompt. Natural language after the mention stays prose,
including `@lark-agent review` without a slash.

Unknown `/foo` posts help when comments are allowed and does not call the
model. Dry-run clears every write name before help or tools run.
