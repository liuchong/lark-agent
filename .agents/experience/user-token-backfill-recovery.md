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

For messages that were already received, conversation history is optional
enrichment and must not block the current event from reaching the model. Mark
the context selection incomplete when history loading fails.

The daemon caches user credentials, but an expired cached access token must not
require a restart. Serialize recovery, reload a newer Keychain token first, then
use the official SDK refresh endpoint only if Keychain still has the failed
token. Refresh tokens rotate once; persist the new refresh token before the new
access token, update process memory only after persistence succeeds, and replay
the original request at most once.
