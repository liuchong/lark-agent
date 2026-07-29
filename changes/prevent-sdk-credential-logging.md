# Prevent SDK Credential Logging

## Goal

Official Lark SDK diagnostics must not write WebSocket access keys, tickets,
tokens, or app secrets into daemon logs.

## Directly Applicable Rules

- "secret、token、私钥和连接凭据不得进入仓库、测试 fixture、日志或提交。"
- Lark production calls remain in `internal/lark` and use the official public
  Go SDK.
- Every behavior change requires an `integration_test/` regression.
- Verification and commit complete before production installation.

## Business Design

Every HTTP and WebSocket SDK client receives one credential-safe logger:

- debug and info calls are ignored;
- warning and error calls remain visible;
- query parameters named `access_key`, `ticket`, `authorization`,
  `client_assertion`, or ending in `token`/`secret` are replaced with
  `[REDACTED]`;
- the same fields in JSON-shaped diagnostics are replaced;
- logging never changes SDK request, reconnect, or event behavior.

The event dispatcher also uses this logger after construction. Its initial
constant ready message is harmless, but all event processing diagnostics follow
the same redaction boundary.

## BDD Acceptance

### Scenario: Successful WebSocket connection

Given a bootstrap response contains unique access-key and ticket canaries,
when the production realtime consumer connects,
then neither canary appears in captured stdout or stderr.

### Scenario: Warning or error contains credentials

Given SDK warning/error arguments contain credential query parameters or JSON
fields,
when the safe logger writes them,
then the diagnostic context remains and every credential value is redacted.

### Scenario: HTTP client uses the same boundary

Given the official HTTP SDK client is constructed,
when it emits SDK diagnostics,
then it uses the same safe logger rather than the SDK default logger.

## Non-Goals

- Removing the agent's own structured lifecycle and error records.
- Hiding non-secret failure reasons needed for operations.
- Modifying the official SDK source.

## Completion

The change is complete when red/green unit and integration tests pass, the full
low-concurrency suite and static checks pass, the fix is committed and pushed,
and a production restart creates no new credential-bearing SDK log line.
