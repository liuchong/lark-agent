# Owner-private control plane

For durable Lark work, lifecycle counts are not a sufficient control surface.
Keep the following boundaries aligned:

- only the configured Owner's assistant-bot private chat may disclose task or
  approval state and apply mutations;
- Owner group commands redirect to private chat without counts or details;
- non-Owner bot commands remain silent;
- slash commands are parsed before model execution and invalid slash commands
  never fall through to the model;
- task lists are bounded and action-oriented, and each actionable item exposes
  an exact next command;
- a command message ID journals the command, validation, mutation, and rendered
  result in one SQLite transaction so duplicate Lark delivery is idempotent;
- interrupted external actions with uncertain results are reconciled by an
  explicit Owner disposition and are never replayed;
- bot commands and local CLI commands reuse the same typed storage queries and
  mutation rules;
- lifecycle notices omit zero categories and point nonzero work to `/tasks`;
- configured Owner language wins; otherwise use the resolved Owner language,
  with one explanatory language per message.

When extending commands, update `spec/behavior.md`, `control.HelpText`, Cobra
help, `docs/operations.md`, parser/storage/app integration tests, and the live
international Lark verification script together.
