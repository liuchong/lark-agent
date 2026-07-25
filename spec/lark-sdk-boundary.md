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

## Resource Monitoring

`subscription add` accepts Wiki and Base URLs and stores a durable
`ResourceSubscription`. Base URLs are normalized to app token, table, and view.
Wiki URLs keep the wiki node token until runtime resolution to the underlying
object token. Base remote subscription is file/app scoped; table is local
filtering context and view is display context only. Document @ monitoring is
limited to public comment APIs and Cloud Docs Assistant notifications. Base
comment/@ events and view-level remote subscriptions are not claimed.
