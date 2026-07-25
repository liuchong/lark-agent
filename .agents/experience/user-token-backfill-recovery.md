# User-token backfill recovery

When delegated group mentions do not get responses, distinguish two states:

- `queue inspect` / `queue resume` only work for messages that already have a
  local intake receipt.
- If the user token was missing, user-identity polling could not see group
  messages, so there is no receipt to resume.

After the user token is configured, recover only with a bounded `queue backfill`
command that specifies the chat and time range. Do not widen the normal poll
lookback to sweep history, because that can turn old messages into unsolicited
work.
