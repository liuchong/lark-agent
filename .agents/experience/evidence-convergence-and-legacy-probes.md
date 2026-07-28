# Evidence convergence and legacy probes

Two live failures established reusable implementation rules.

## Coding evidence convergence

- A prompt that mentions a turn budget is not sufficient enforcement.
- Once a coding run has digest-backed production evidence, tell the model to
  answer if the requested fields are covered.
- On the immediately following model turn, expose only `submit_decision`.
  Reject any model-emitted non-terminal call without execution and keep a
  bounded repair turn. Waiting until the final two turns still allows long,
  unrelated investigations after the answer is already known.
- Do not require production call-site reachability when the user only asks for
  a named function's direct behavior.

## Explicit reply confidence

- A missing numeric field is not evidence of low confidence.
- Require `reply_confidence` explicitly for every `reply`. Reject omission
  inside the model loop so the model can repair it.
- Preserve the configured approval policy only for an explicit confidence
  value below the threshold; never convert omission to zero and silently hold
  a useful reply.

## Legacy approval lookup

- Compatibility metadata is meaningful only after a matching legacy action is
  proven to exist.
- Query the current action key first, then the legacy action key. Only when a
  ready legacy action exists should code load and validate legacy relevance.
- A retry decision with empty relevance and no legacy action means "no approval
  to consume", not an identity error.
- When a legacy action does exist, missing or mismatched relevance remains a
  hard failure so old approvals cannot change reply identity.
