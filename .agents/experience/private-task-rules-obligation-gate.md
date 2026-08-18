# Private task rules and the obligation gate

Owner policy lives in a local Markdown file beside config. Go owns only the
mechanism: load one snapshot, prove or reject an owner obligation, and keep
security, approval, workspace, and send identity above that file.

- A group `@Owner` mention is a candidate, not a must-reply. Informational
  announcements and discussion opinions without an action request complete as
  `no_reply_needed`. The main Agent does not run, and the owner does not get a
  "task stopped" notice.
- Unanswered work needs proven obligation: an exact quote from the target, or
  an exact quote from the current snapshot. Private rules cannot invent message
  text. An explicit request in the target still wins over a private "ignore
  this class" sentence.
- Do not compile workplace catalogs into business code, fixtures, logs, or
  SQLite. Persist digest and obligation quotes only.
- Enabled but missing, unreadable, oversized, or escaped files fail closed:
  no sender-facing send, one owner file-fault diagnosis, no reuse of an old
  snapshot. A digest change cancels any held unsent draft and reclassifies it.
- Existing installs stay disabled until `lark-agent rules init` or a new
  `lark-agent init`. `/rules` and `rules check` never print the body or an
  absolute path.

The retry-ceiling error used to contain the word "context", so owner notices
wrongly said the conversation context was incomplete. Localize
`owner_reply_ambiguous`, `did not converge`, and `task_rules_unavailable`
before any generic "context" match.
