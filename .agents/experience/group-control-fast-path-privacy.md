# Group control fast-path privacy

Owner-private task state includes task counts, work IDs, approval commands,
queue summaries, detailed help, recovery hints, and "why did/didn't it reply"
diagnostics. In group chats, both slash commands and natural/fast-path variants
such as `status`, `doctor`, `queue summary`, `help`, and response-status
questions must redirect to the assistant private chat without exposing these
details.

Do not treat "not a slash command" as safe. Time, date, `ping`, and simple
availability checks are safe group fast paths because they do not read queue
state.
