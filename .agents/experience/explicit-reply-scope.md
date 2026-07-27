# Explicit delegated reply scope

Do not infer a production reply boundary from an operational discovery flag.
`--chat-query` discovers and marks groups for polling and validation, while
`policy.reply_scope` independently decides whether delegated owner replies are
allowed in all visible groups or only those marked groups.

When these concerns are coupled, intake and model evaluation can succeed while
the final action is silently blocked. Keep the scope in typed configuration,
validate restricted mode requires a discovery query, expose the effective value
in doctor output, and test the path through the reply controller.

Changing scope must not replay historical terminal or interrupted work.
