# Explicit reply scopes

Do not infer a production reply boundary from an operational discovery flag.
`--chat-query` discovers and marks groups for polling and validation, while
`policy.reply_scope` independently decides whether delegated owner replies are
allowed in all visible groups or only those marked groups.

Group assistant invocation is a separate product entry point and must have a
separate `assistant.reply_scope`. It decides whether any human can natively
mention the assistant in every bot-visible group or only in groups resolved
from `--chat-query`. Restricted assistant scope must resolve the query with bot
identity before real-time intake starts; delegated owner scope continues to use
user-visible polling metadata. Never reuse sender-is-owner as an assistant
mention gate.

Do not reuse one persisted scope boolean for bot-visible assistant groups and
user-visible delegated groups. Carry separate normalized-event markers through
real-time intake, polling, durable storage, routing, and the final reply gate.
Bot chat discovery must consume every `has_more` page with bot identity before
either intake path starts.

When these concerns are coupled, intake and model evaluation can succeed while
the final action is silently blocked. Keep the scope in typed configuration,
validate restricted mode requires a discovery query, expose the effective value
in doctor output, and test both intake and the path through the reply controller.

Changing scope must not replay historical terminal or interrupted work.
