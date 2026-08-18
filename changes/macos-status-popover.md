# macOS Status Popover

## Confirmed Scope

The menu bar app stays a thin local controller. Left-clicking the status item
opens a structured popover of runtime details. Right-click keeps the existing
Start/Stop/Restart control menu. Raw command JSON is not the primary status
surface.

The user asked for a status surface they can open from the menu bar. This
change does not add a web UI, SwiftUI, or a new daemon command.

## Directly Applicable Hard Rules

- `任何方案必须先经用户确认再实施。`
- `所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按 red -> green -> refactor 实现。`
- `每项逻辑变化必须有 integration_test/ 覆盖；没有相应集成测试时不得宣称完成。`
- Public repository artifacts must not contain real people, chat/message/user
  identifiers, private repository names, private filesystem paths, secrets, or
  copied production conversation text.
- `secret、token、私钥和连接凭据不得进入仓库、测试 fixture、日志或提交。`

## Non-goals

- Do not run full `doctor` on the 10-second menu-bar refresh.
- Do not display API keys, tokens, app secrets, private task-rule bodies,
  approval `request_json` / `response_json`, or owner open IDs.
- Do not change LaunchAgent install paths, the template status icon, or the
  existing Start/Stop/Restart/Pause/Auto commands.
- Do not mix this change with unrelated remote-PR or model-failure work.

## Business Design

### Surfaces

- Left-click: `NSPopover` with sectioned AppKit layout.
- Right-click: existing control menu.
- Periodic refresh: `daemon status`, `approval status`, and `queue summary`
  only, so the icon badge stays current without Keychain/SDK doctor work.
- Opening the popover loads those cheap commands immediately, then may load
  `doctor` and a bounded approval list to fill remaining sections.

### Panel sections

1. Service: running/stopped/not installed, loaded, pid, mode, last error.
2. Queue: status counts including interrupted and dead_letter, stale
   processing, and lane counts.
3. Approvals: awaiting count and bounded rows of id, kind, work item, status.
4. Task rules: public view only (`enabled`, `status`, `file_name`, `bytes`,
   truncated digest).
5. Reply scopes: assistant, owner, and private scopes plus owner-wait.
6. GitHub: enabled, read-only, and whether a token is configured. Never the
   token value.
7. Recent work: work item id, kind, status, duration, model turns. Message IDs
   are omitted from this surface.

Command results appear as a one-line banner in the popover when it is open,
or as a one-line alert summary when the owner used the right-click menu.

### Failure

If a command envelope is not `ok`, the matching section shows a structured
error message from the envelope, not the raw combined stdout/stderr dump.
JSON parsing uses stdout only.

## BDD

Given the menu bar app is running
When the owner left-clicks the status item
Then a popover lists structured service, queue, approval, task-rule public,
reply-scope, GitHub non-secret, and recent-work rows
And the primary layout is not a raw JSON dump
And secret and request bodies are absent from the panel source

Given the menu bar app is running
When the owner right-clicks the status item
Then Start, Stop, Restart, Pause, Resume Auto, Open Config, Open Logs, and
Quit remain available

Given the 10-second refresh timer fires
When the popover is closed
Then the app does not invoke `doctor`

## Verification

- Source-contract integration tests for popover, sections, template icon, and
  secret/request-json absence.
- `swiftc macos/LarkAgentStatus/*.swift -framework AppKit` compile check.
- Existing installer tests still compile and install the status app.
