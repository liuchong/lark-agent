# Menu bar status is a structured panel, not JSON

The macOS menu bar app is a thin local controller. Left-click must open a
sectioned popover of runtime details. Right-click keeps Start/Stop/Restart.
Do not paste `daemon status` or `queue list` JSON into `NSAlert` as the primary
status surface.

- Icon refresh every 10 seconds may call only `daemon status`, `approval status`,
  and `queue summary`. Full `doctor` belongs on popover open or explicit refresh.
- `approval status` must return counts plus a bounded public pending preview.
  Never call unbounded `approval list` from the status app: dumping
  `request_json` fills the stdout pipe and `waitUntilExit` before reading
  deadlocks the panel on “正在加载诊断…”.
- Read child-command stdout to EOF before waiting for exit.
- First paint should already show service, pending approval rows, attention
  queue counts, and recent work. Historical ignored/cancelled totals are a
  summary line, not the primary tiles.
- Parse command JSON from stdout only. Never merge stderr into the envelope.
- Show task-rule public fields, GitHub `token_configured`, and work item ids.
  Never show tokens, app secrets, rule bodies, owner open IDs, or approval
  request/response bodies.
- Keep the template status icon and `LSUIElement` accessory app. Compile every
  `macos/LarkAgentStatus/*.swift` file together.
