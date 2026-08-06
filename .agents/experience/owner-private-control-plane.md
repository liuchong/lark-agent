# Owner-private control plane

For durable Lark work, lifecycle counts are not a sufficient control surface.
Keep the following boundaries aligned:

- only the configured Owner's assistant-bot private chat may disclose task or
  approval state and apply mutations;
- Owner group commands redirect to private chat without counts or details;
- non-Owner bot commands remain silent;
- slash commands are parsed before model execution and invalid slash commands
  never fall through to the model;
- slash parsing, `/help`, detailed help, and semantic natural-language control
  must share one canonical command catalog;
- owner-assistant private context retains bounded messages authored by the
  current assistant app, because short confirmations need their exact adjacent
  notice; group context still filters unpinned app noise;
- a command-shaped semantic result below the read-only confidence floor falls
  through to the ordinary business-question path, while high-confidence
  ambiguity uses deterministic candidate-ID wording rather than model prose;
- task lists are bounded and action-oriented, and each actionable item exposes
  an exact next command;
- creating a durable reply approval is not enough: the first actionable private
  notice must say the draft is unsent and include the typed approval action ID,
  exact draft, remaining owner work, and exact approve/reject commands; use an
  action-derived idempotency key so notification retries do not create noise,
  and label assistant-facing drafts separately from delegated reply drafts;
- a high-confidence delegated reply remains autonomous: notify the owner before
  the sender-facing reply, then send immediately without waiting for approval;
  perform one side-effect-free owner-handled/withdrawn preflight before that
  notice and repeat the check immediately before the external reply;
- an approved delegated draft is no longer awaiting approval: even when the
  global gate is configured in approval mode, notify the owner before consuming
  the exact approval and sending the sender-facing reply;
- a command message ID journals the command, validation, mutation, and rendered
  result in one SQLite transaction so duplicate Lark delivery is idempotent;
- interrupted external actions with uncertain results are reconciled by an
  explicit Owner disposition and are never replayed;
- bot commands and local CLI commands reuse the same typed storage queries and
  mutation rules;
- lifecycle notices omit zero categories and point nonzero work to `/tasks`;
- configured Owner language wins; otherwise use the resolved Owner language,
  with one explanatory language per message.
- automatically extracted memory remains an unconfirmed candidate until the
  Owner confirms it, and storage rejects provider tokens, cloud keys,
  private-key material, and password/secret assignments on every entry path.
- task content must resolve Lark's internal `@_user_N` placeholders at the
  control rendering boundary: use stored display names for known mentions and
  a localized generic person label for unknown mentions. Never pass raw
  placeholders into durable reply validation, because the reply will be
  rejected instead of reaching the Owner.

When extending commands, update `spec/behavior.md`, `control.HelpText`, Cobra
help, `docs/operations.md`, parser/storage/app integration tests, and the live
international Lark verification script together.
