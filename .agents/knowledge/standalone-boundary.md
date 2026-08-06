# Standalone boundary

`lark-agent` is an independent Go module and application. Its only Lark
production boundary is the official public Go SDK
`github.com/larksuite/oapi-sdk-go/v3`.

Production invariants:

- no production code executes `lark-cli` or parses CLI stdout/NDJSON as a Lark protocol;
- all SDK calls originate in `internal/lark`;
- app secret remains in macOS Keychain or a test-only environment variable, never YAML;
- user tokens are optional and only enable user-identity polling and delegated replies;
- SDK HTTP and WebSocket events are projected into typed Agent DTOs at the boundary;
- an unknown successful response is an error, not permission to guess;
- no import of `github.com/larksuite/cli` is allowed;
- document/Base subscriptions use only public SDK-backed APIs; comment
  notifications and Base record-change events are accepted as signals, while
  table/view filtering and current record verification remain local and typed.

Standalone local ownership:

- config: `~/.config/lark-agent/config.yaml`;
- state: `~/Library/Application Support/lark-agent/state.db`;
- logs: `~/Library/Logs/lark-agent`;
- LaunchAgent: `com.liuchong.lark-agent`;
- no old command-line tool directory is read or migrated;
- cross-session stateless read-only work is automatically readmitted only after
  a ready session classifies it as safe to recompute; delegated conversation
  context and unsent reply candidates require explicit owner resume and new
  classification;
- executing or result-uncertain external actions are never auto-replayed; they
  are terminalized with one durable owner reconciliation notice;
- terminal history is only replayed through an explicit owner command.

Repository rewrite and runtime transport changes must preserve these invariants
in specifications, tests, help, documentation, installation, and live evidence.
