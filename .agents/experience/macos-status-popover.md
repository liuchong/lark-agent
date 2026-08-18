# Menu bar status is a structured panel, not JSON

The macOS menu bar app is a thin local controller. Left-click must open a
sectioned popover of runtime details. Right-click keeps Start/Stop/Restart.
Do not paste `daemon status` or `queue list` JSON into `NSAlert` as the primary
status surface.

- Icon refresh every 10 seconds may call only `daemon status`, `approval status`,
  and `queue summary`. Full `doctor` belongs on popover open or explicit refresh.
- Parse command JSON from stdout only. Never merge stderr into the envelope.
- Show task-rule public fields, GitHub `token_configured`, and work item ids.
  Never show tokens, app secrets, rule bodies, owner open IDs, or approval
  request/response bodies.
- Keep the template status icon and `LSUIElement` accessory app. Compile every
  `macos/LarkAgentStatus/*.swift` file together.
