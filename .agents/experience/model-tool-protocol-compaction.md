# Model tool protocol compaction

An assistant message with tool calls and all matching tool-result messages is
one provider protocol unit.

- Never insert a system, user, or assistant message between sibling tool
  results from the same assistant turn. Accumulate progress and convergence
  prompts and append them after every sibling result.
- A context compaction boundary must not start inside the latest complete tool
  unit. Move the boundary back to its assistant message, then clip result
  content while preserving tool-call IDs and ordering. Replace oversized
  historical arguments with valid JSON containing their original byte count
  and digest; do not use a raw byte slice that can invalidate JSON.
- A checkpoint for an older parallel unit must emit one entry per tool call and
  include the call ID on both call and result entries. Keeping only
  `ToolCalls[0]` makes same-tool sibling results ambiguous.
- Reserve room for runtime progress and terminal prompts, stabilize the
  reported model-visible byte count after those prompts are appended, and
  enforce the final request limit. If protocol IDs and names alone cannot fit,
  fail locally before another provider request.
- Reproduce provider `invalid parameter` failures with a protocol-checking test:
  one assistant turn emits at least two calls, one result is large enough to
  trigger compaction, another result fails, and the next model request verifies
  that no call or result is orphaned.
- A provider accepting the first request does not prove the tool schema is at
  fault when a later request fails. Inspect the exact post-compaction message
  sequence before changing schemas or credentials.
- A tool receipt result digest identifies the whole tool output; a source
  digest identifies one concrete file or reference. Models can confuse them
  when both are visible. Canonicalize only by a unique current-run
  `(source kind, relative path)` identity, and keep multiple observed digests
  ambiguous rather than guessing a version.
