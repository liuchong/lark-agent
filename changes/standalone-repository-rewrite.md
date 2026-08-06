# Standalone Repository Rewrite

## Goal

`lark-agent` is a single independent Go module. Its only Lark production
boundary is `internal/lark`, which calls the official public Go SDK
`github.com/larksuite/oapi-sdk-go/v3` directly.

## Required Behavior

- Preserve owner-only direct requests, delegated replies, contextual
  conversations, coding tools, scheduler lanes, reactions, and lifecycle notices.
- Durably distinguish live, duplicate, offline-backlog, interrupted, completed,
  and uncertain work.
- After a ready-session gate, only stateless work may be recomputed
  automatically. Delegated context, drafts, and uncertain side effects are
  never replayed across restarts without exact owner authority.
- Install from the current independent config and state only; historical data is
  not imported.
- Build, verify, commit, and push before installing live service changes.

## Implemented Boundary

- No production package imports `github.com/larksuite/cli`.
- Lark app secret lives in Keychain or a test-only environment variable, never
  YAML, plist, fixtures, or logs. User tokens are optional and only enable
  user-identity polling and delegated replies.
- SDK HTTP responses and WebSocket events are projected into typed Agent DTOs in
  `internal/lark`; missing required fields fail explicitly.
- The macOS service uses `com.liuchong.lark-agent`, independent config/state
  directories, install rollback, and ready-session gate.

## Non-Goals

No second repository, copied upstream internal packages, historical data import,
Linux/Windows service, plugin ABI, release tag, or automatic editing of business
Base/document records is part of this rewrite.
