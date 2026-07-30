# Evidence convergence and legacy probes

Two live failures established reusable implementation rules.

## Coding evidence convergence

- A prompt that mentions a turn budget is not sufficient enforcement.
- Once a coding run has digest-backed production evidence, tell the model to
  answer if the requested fields are covered.
- Treat search and code-index sources as candidate locations, not authoritative
  reads. They must not trigger convergence before `read_workspace` has actually
  returned the production file.
- Enforce the same distinction at the terminal verifier. A definite coding
  reply must cite at least one production source from an authoritative read;
  prompt guidance and convergence logic alone do not stop a model from
  submitting a search-only guess.
- Do not infer insufficient evidence from free-form reply substrings. Require a
  structured evidence status, and canonicalize insufficient coding replies so
  an unknown phrase cannot carry an unsupported definite inference.
- Preserve a useful negative search result only from runtime-parsed tool
  receipts: every parsed bounded search must report zero matches, while any
  positive match or unparseable report falls back to the canonical
  insufficient-evidence reply. State the actual query, scan counts, and
  truncation status only when every required receipt field is explicit,
  correctly typed, non-null, and non-negative where numeric. If the receipt set
  exceeds the documented display bound, count repeated receipts too and fall
  back instead of silently truncating it. Set that bound from observed normal
  investigation shapes, not an arbitrary number that discards otherwise useful
  evidence. Never turn a bounded zero result into a global nonexistence claim.
- A canonical insufficient reply is not a substitute for investigation. Require
  at least one successful workspace/code evidence tool before accepting it;
  Lark-history reads alone do not count.
- Apply evidence gates to every terminal branch. For code-fact questions,
  require `reply` and reject `ignore`, `record`, `notify`, and
  `request_approval` so no terminal type can bypass authoritative reads or
  turn an answerable prompt into silence.
- An authoritative production read proves only the facts contained in that
  source; it does not prove that a multi-field investigation is complete.
  Prompt the model to compare the read against every requested field and plan
  stop condition. Keep bounded code tools available for unanswered fields, and
  reserve the final two model turns for terminal submission and one bounded
  correction once citable evidence exists. Exhausted tool or no-progress
  budgets also force terminal-only mode.
- When the user names a repository or workspace-relative path, preserve its
  exact spelling and case. Do not silently substitute a similarly named sibling
  project merely because a broad search finds it first. Resolve relative paths
  only against known top-level workspace directories, and only when one such
  path is present so comparisons are not collapsed onto the first repository.
  Inherit a scope from conversation context only for an explicit continuation
  from the same sender. Reject global index/trace tools that cannot carry the
  exact path boundary.
- A hard subtree boundary must remain usable. Canonicalize the configured
  workspace directory prefix, inject the known scope into path-capable search
  and list tools, and make the search tool expose that path in its real schema.
  Prompt-only path requirements that the tool cannot express create a
  guaranteed nonconvergence loop.
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
