# Investigation Turns And Terminal Finalizer

## Problem

Work `#5680` reached terminal-only repair after broad coding search produced no
reliable source. The model then called unavailable tools for three terminal
attempts instead of `submit_decision`, so the work dead-lettered with no
sender-facing partial result.

## Behavior

- Coding-question model turns default to `100`. This is an upper bound, not a
  target; tool, no-progress, repeated-result, context, and evidence gates still
  force early convergence.
- Each model request continues to include current turn, maximum turns,
  remaining turns, tool budget, context budget, and urgency. Prompt text must
  encourage the shortest reliable investigation rather than spending all turns.
- When terminal-only attempts are exhausted, runtime makes one independent
  no-tool finalizer request over retained tool receipts and failures.
- The finalizer must emit the same structured decision shape as
  `submit_decision`. It cannot execute tools, invent source references, or lower
  coding evidence requirements.
- A valid finalizer decision completes normally through existing quality,
  evidence, permission, approval, and send gates. An invalid finalizer preserves
  the current dead-letter path with a more precise terminal reason.

## BDD Acceptance

- Given default configuration, coding questions receive `100` model turns and
  prompts still show current and remaining turns.
- Given a model ignores terminal-only instructions by calling unavailable tools,
  when the terminal attempt limit is exhausted, then one no-tool finalizer
  request is made before dead-lettering.
- Given the finalizer returns a partial insufficient coding decision grounded
  only in runtime tool receipts, then the decision completes and exposes
  completed checks, unknowns, and next step.
- Given the finalizer claims a path or source not produced by a successful tool,
  then existing source validation rejects it and the work dead-letters.
- Given evidence is found early, the model may submit a complete result in far
  fewer than `100` turns.

## Non-goals

- Do not raise `tool_policy.coding_max_tool_calls` in this change.
- Do not let the finalizer run workspace, shell, Lark, GitHub, or any other
  tool.
- Do not auto-resume historical work `#5680`.
