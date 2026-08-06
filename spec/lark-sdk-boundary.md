# Lark SDK Boundary

`internal/lark` is the only production Lark boundary. It constructs official
`github.com/larksuite/oapi-sdk-go/v3` clients, converts SDK responses and
WebSocket events into Agent-owned typed values, and returns structured Agent
errors. Production code must not execute `lark-cli`, parse CLI stdout/NDJSON, or
import `github.com/larksuite/cli`.

## Identity And Credentials

YAML stores `lark.app_id`, optional API base URL, and Keychain references only.
The app secret lives in macOS Keychain. User access and refresh tokens are
optional Keychain entries used only for user-identity polling and delegated
replies. Test processes may inject `LARK_AGENT_APP_SECRET` and
`LARK_AGENT_USER_ACCESS_TOKEN` instead of touching a real Keychain. Refresh
failure must stop user-identity write operations instead of silently downgrading
to anonymous identity.

## Events And Messages

Bot-visible owner private messages and owner-authored group @mentions arrive
through SDK WebSocket `im.message.receive_v1` handlers. Lark international apps
may also deliver the same message callback through the legacy `message` event
key; `internal/lark` projects both event shapes into the same typed Agent
message before intake. If a real-time message event lacks a trusted create time,
the boundary uses the WebSocket receive time for intake freshness so a live
owner request is not misclassified as offline backlog. User-visible polling and
delegated replies use user access tokens when configured; without a user token,
bot WebSocket owner requests still run but user-identity polling is disabled.
Both paths write intake receipts before work is claimable, so duplicate
WebSocket or polling observations cannot rerun model work or repeat replies.

GitHub workflow notifications use the same official SDK boundary through an
HTTP-only bot message send. The notification process must not create an SDK
WebSocket client. Its stable message UUID is validated before the SDK call and
its typed result contains the sent message ID and chat ID.

A quoted GitHub marker is trusted only when the corresponding same-chat
relation message was authored by the configured current Lark app, its
HMAC-SHA256 signature verifies with the same Lark app secret, and the
repository is allowlisted. Human-authored, other-app, adjacent,
copied-through-bot, malformed, unsigned, or conflicting markers never
establish external tool authority.

## Resource Monitoring

`subscription add` accepts Wiki and Base URLs and stores a durable
`ResourceSubscription`. Base URLs are normalized to app token, table, and view.
Wiki URLs keep the wiki node token until runtime resolution to the underlying
object token. Base remote subscription is file/app scoped; table is local
filtering context and view is display context only. Document @ monitoring is
implemented through public comment notification events, typed comment reads,
and Cloud Docs Assistant notifications. Base record-change events are accepted
at file/app scope and filtered to the configured table locally; view-level
remote subscriptions are not claimed. Every event is only a signal: current
comment, schema, and record state is re-read before work or mutation.
