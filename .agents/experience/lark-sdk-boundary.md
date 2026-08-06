# Lark SDK boundary experience

- Use `internal/lark` for every production Lark call. It owns SDK client
  construction, Keychain credential lookup, HTTP response projection, WebSocket
  event projection, and structured Agent errors.
- YAML may contain `app_id`, domain override, and Keychain references only.
  Secrets and tokens belong in Keychain; tests may use `LARK_AGENT_APP_SECRET`
  and `LARK_AGENT_USER_ACCESS_TOKEN`.
- Local tests must not contact real Lark. Use fake callers, `httptest`, typed
  event fixtures, or `LARK_AGENT_OFFLINE_LIVE_TEST=1` for isolated LaunchAgent
  readiness tests.
- Resource monitoring must state platform limits explicitly: Base remote
  subscription is app/file scoped, table filtering is local, view is context
  only. Public comment notifications and Base record-change events are signals,
  not record truth; re-read the typed comment, schema, and record before work or
  mutation, and keep application-authored notification prose untrusted.
