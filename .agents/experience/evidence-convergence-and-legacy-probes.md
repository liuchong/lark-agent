# Evidence convergence and legacy probes

Two live failures established reusable implementation rules.

## Coding evidence convergence

- A prompt that mentions a turn budget is not sufficient enforcement.
- Once a coding run has digest-backed production evidence, tell the model to
  answer if the requested fields are covered.
- Reserve the final two coding turns for `submit_decision` when such evidence
  exists. Hide other tools and reject any model-emitted non-terminal call so one
  final repair turn remains.
- Do not require production call-site reachability when the user only asks for
  a named function's direct behavior.

## Legacy approval lookup

- Compatibility metadata is meaningful only after a matching legacy action is
  proven to exist.
- Query the current action key first, then the legacy action key. Only when a
  ready legacy action exists should code load and validate legacy relevance.
- A retry decision with empty relevance and no legacy action means "no approval
  to consume", not an identity error.
- When a legacy action does exist, missing or mismatched relevance remains a
  hard failure so old approvals cannot change reply identity.
