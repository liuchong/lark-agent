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
- Make an inherited exact scope operational before the first tool call. Tell
  the model that it is already a readable subtree of the configured workspace,
  expose a positive allowlist of path-scoped workspace, same-chat context, and
  terminal control tools, and accept repository-relative paths by safely
  prefixing the exact scope. A blacklist misses workspace-rule and skill
  readers that can still cross the requested repository boundary.
- Lexical scoping is not enough when the configured workspace contains
  siblings. Resolve the exact scope and candidate real paths before list,
  search, or read execution; reject a scoped symlink that resolves into a
  sibling, and verify each existing path component's on-disk case.
- Preserve the differently cased sibling trap: `sample-client/...` may be relative
  to `sample-project/sample-module`, while `Sample-Module/...` remains an explicit
  substitution error.
- A mandatory investigation plan is control metadata, not evidence work. Do
  not charge it against the investigation-tool budget; report current, total,
  and remaining investigation calls beside model-turn and context budgets so
  multi-fact questions reserve enough calls for authoritative reads.
- Do not require production call-site reachability when the user only asks for
  a named function's direct behavior.
- A language-level opaque container such as `String`, bytes, or raw JSON proves
  only the container type, not the concrete serialized shape. Keep a bounded
  documentation, fixture, protocol, or serializer read available when shape is
  part of the question, and enforce structural evidence at the terminal gate
  instead of relying on prompt compliance alone.
- Detect shape intent per business-message semantic unit, not by concatenating
  an entire conversation into one keyword bag. Historical `String`/JSON words
  must not combine with an unrelated current field-declaration question. Use
  prior context only when the current message itself explicitly asks for
  structure/format and needs one linked or nearest relevant message to resolve
  the serialized target. Distinguish that semantic request from instructions
  about response formatting or layout; presentation instructions never borrow
  a serialized target from history. Treat imperative requests such as
  "supplement the concrete structure" as real shape follow-ups, and match
  English container words as complete tokens so identifiers such as
  `StringUtils` do not become accidental targets.
- Do not let the generic final-two-turn convergence rule consume that bounded
  structural read. If the penultimate turn starts with only opaque evidence,
  expose exactly one `read_workspace` call for an already-known current
  documentation, fixture, protocol, or serializer path, then expose only
  `submit_decision` on the final turn. Enforce the per-turn tool catalog at
  execution time too; hiding a tool from the schema is not enough if a model
  emits a stale or guessed tool name.
- Do not wait for the penultimate turn when the structural source was never
  located. Once a current read proves the named field is opaque and at least
  two turns remain, force one exact field-name search across the whole already
  selected repository scope. Reject child paths and case variants instead of
  letting model-supplied search arguments narrow the recovery, and reject a
  model-supplied result limit that could truncate candidates. Treat this one
  recovery search separately from the generic source-less-search limit: prior
  locating failures must not consume the only structural recovery attempt.
  Likewise, do not apply the plan-first broad-search gate when a direct read
  already exposed the opaque target and the runtime itself selected this exact
  field-name recovery; keep that gate for model-chosen broad searches.
  Keep only result paths whose local snippet binds that field to a concrete
  structure, then force one read from that candidate set before accepting an
  insufficient decision. Validate the search query, full scope, result limit,
  and read path at execution time; prompt wording alone does not stop a model
  from spending the recovery turn on a nearby listener or callback.
- Bind structural evidence to the concrete lower-camel field nearest the shape
  request. A JSON object or serializer elsewhere in the same concatenated read
  set is unrelated evidence. Accept a same-line field example or a field
  introduction that explicitly names JSON, schema, shape, or example and is
  followed locally by the structure in the same source; never associate
  evidence across source boundaries. A line saying the target is unknown or
  undefined terminates that association, and a following structural
  introduction for another lower-camel field starts a different context.
  Validate the reply's exact inline JSON against those same field-related local
  snippets, not against the union of all cited source contents.
- Multi-fact protocol answers need two JSON checks rather than one overloaded
  comparison. First require every concrete reply JSON example to occur in the
  union of cited current-run reads. Then extract only reply contexts locally
  bound to the named shape target and require those examples to occur in that
  target's local evidence. Comparing response, push, or local-state JSON
  directly against a `sampleContent`-only snippet creates false insufficiency;
  checking only the union permits unrelated JSON to masquerade as the target.
- Final grounding failures are reply-local corrections, not evidence loss. Keep
  one bounded submit-only correction turn outside the investigation budget,
  retain authoritative read contents in runtime state, and reject an immediate
  downgrade that falsely blames context compaction. Still permit a precisely
  stated, genuinely new evidence gap to converge as insufficient. This lets the
  model remove an unsupported identifier such as `eventCode` and preserve the
  supported fact "notification 9001" without reopening tools or weakening
  identifier checks.
- Validate outward repository paths against current-run `read_workspace`
  sources, not all submitted citations; a search receipt remains a locator even
  when the model copies it into `source_refs`. Validate lower-camel-case
  identifiers as complete identifiers against the cited read contents. This
  catches plausible but wrong nearby names such as `modifyTime` versus
  `sampleTimestamp` and prevents an unread test path from being presented as
  inspected evidence.

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
