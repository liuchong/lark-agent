# Lark Agent Behavior Specification

This document is the long-lived behavior contract for `lark-agent`. It records
the system that is implemented after merge, not a temporary design note.

## Product Goal

`lark-agent` is an independent personal Lark assistant runtime built on the
official public Go SDK `github.com/larksuite/oapi-sdk-go/v3`. In
`daemon run --live` it runs a local durable queue, consumes bot-visible owner
requests through Lark real-time events (`im.message.receive_v1` and the legacy
`message` callback shape observed from Lark international apps), polls
user-visible group and private messages as a fallback and for delegated owner
mentions, decides whether an event is relevant to the owner, and may reply as
the owner when the configured policy allows it. It does not execute `lark-cli`,
import official CLI Go packages, copy official internal commands, or store Lark
secrets in YAML.

The agent is built as deterministic Go runtime plus an Eino model loop. The
model is only one part of the system: queueing, routing, workspace boundaries,
identity checks, idempotency, audit, and rollback decisions are enforced by
code outside the prompt.

## Harness Architecture

The runtime is split into explicit layers so a slow or divergent model run
cannot hold the whole Lark assistant hostage:

- Intake normalizes user-token polling and bot-visible real-time events into
  the same durable event shape, dedupes by message ID, and stores the event
  before any model call or reply side effect.
- Router and fast path are deterministic. They classify owner-only assistant
  requests, direct owner mentions, simple local questions, coding questions,
  and long-running coding goals before a model run is selected.
- The scheduler claims work by lane and priority instead of a single FIFO
  stream. Post-reply owner notices and fast owner requests are foreground
  lanes; coding questions have bounded foreground budgets; coding goals run in
  a background lane. At least two foreground workers are required and one is
  reserved for fast/simple owner requests.
- The agent core receives a work kind, a turn budget, and tool policy receipts.
  It may ask the model to reason, but Go enforces terminal decisions, budgets,
  source requirements, and no-progress convergence.
- Tool policy is centralized. Shell, workspace reads, code search, Lark context,
  and exploration tools all return bounded receipts and structured denial
  results when a call is outside the current work kind or permission policy.
- State and recovery track work kind, priority, online session, lease, run
  status, tool progress, final action, and duplicate links. Restart recovery
  snapshots stale work at its last durable stage and pauses it before new work
  is claimed. It never silently turns an interrupted message back into runnable
  work.

This harness boundary is product behavior, not an implementation detail. The
model may propose a next step; the harness decides whether that step is allowed,
which lane it belongs to, and how it is recovered after interruption.

## Multi-step Agent Loop

After deterministic safety routing, the model receives one bounded environment
snapshot and decides whether it can submit a final decision immediately or
needs more evidence. Evidence gathering is iterative: each assistant turn may
call registered OpenAI-compatible tools, consume their results, and choose the
next tool until it calls `submit_decision`.

The initial snapshot contains the operating system, configured workspace,
available tools and common commands, a depth- and size-bounded directory
overview, workspace rule paths, skill summaries, the current Lark message, and
recent conversation context. It does not pre-guess code-search terms from the
message. Code questions are investigated by the model using search, read,
rules, skills, Lark-context, and shell tools.

The loop has hard limits for model turns, elapsed time, individual and
cumulative tool output, and repeated no-progress calls. Reaching a limit never
creates a guessed answer. The work item becomes retryable, dead-lettered, or a
source-backed notify/approval decision according to the failure type.

Fast-path work does not enter this loop. A configured owner asking deterministic
local questions such as time, date, ping, daemon status, help, doctor, or queue
summary receives a local answer through the normal reply runtime without a
coding investigation. A simple non-coding owner question may use at most the
simple-agent budget and cannot call shell. A coding question has a separate
foreground budget. When useful evidence exists and the coding run approaches
its budget, the runtime forces a truthful partial answer or clarification
instead of continuing broad search.

`submit_decision` is the only terminal model tool. It has no external side
effect. Its structured output is validated by Go and then passed to the normal
reply or notification policy. Lark reply and notification tools are never
registered as model-callable tools. The native tool schema constrains `risk` to
`low`, `medium`, `high`, or `forbidden`; explanatory text belongs in `reason`,
so an invalid risk sentence cannot turn a terminal decision into a failed tool
round trip.

The model acts on behalf of the configured human owner; it is not a group bot
persona. A message that directly mentions the owner is addressed to this
personal-assistant workflow even when it is a status update, coordination
request, commitment, or follow-up rather than a grammatical question. Such a
message may still need evidence before choosing an outcome. `ignore` is for
content with no owner-relevant information or action. `record` preserves an
owner-relevant update that needs no interruption, `notify` surfaces an owner
action or coordination need, `reply` sends a source-backed owner response, and
`request_approval` holds an exact risky or uncertain action. App/bot messages
in conversation context are evidence only and never redefine the agent's
identity.

A successful `reply` to another sender is a compound user-visible outcome:
first the user-identity reply is sent to the original Lark message, then the bot
privately tells the owner that it replied and summarizes the remaining owner
action. An owner-request addressed to the assistant bot in either a private or
group conversation is instead replied to directly with bot identity and does
not send a redundant post-reply notice to the same owner. The private notice
must not claim a reply was sent when the
group reply failed, was blocked, was cancelled, or is still awaiting approval.
Standalone `notify` means no
sender-facing reply could safely be sent; it is not a substitute for this
post-reply owner notice.

Every terminal `reply` requires a configured reply executor. A missing executor
is a retryable runtime error, never permission to persist a successful reply
decision without an external message. Normal auto replies, not only approval
resumes, are recorded as durable idempotent reply actions before calling Lark
and completed with the returned message ID or exact error.

For owner requests addressed directly to the assistant, the bot adds the
keyboard working reaction to the owner's source message after deterministic
routing and before fast-path or model work starts. Lark names this keyboard
reaction `Typing` in its API; it is a message reaction, not a timer or native
typing-status signal. This applies to the assistant private chat and to
owner-authored group messages that mention the assistant bot. The reaction is
removed only after reply, ignore, approval, failure, or cancellation reaches
its terminal outcome; no fixed display delay controls the lifecycle. It is not
used when another sender mentions the owner and the agent is acting as the
owner's delegate. Reaction cleanup is audited and retried without resending an
already completed reply.

The post-reply owner notice is a separate durable action with a stable Lark
idempotency key. If it fails after the group reply succeeds, retry resumes only
that private notice from its persisted decision; it must not rerun the model or
send the group reply again.

The structured decision separates `reply_text` from `owner_action`.
`reply_text` is the exact sender-facing message. `owner_action` is the concise
private follow-up for the owner; it must not reuse an internal classifier label
such as `direct_mention`. A `request_approval` for a response must include an
exact `reply_text`; the runtime persists that draft and its `owner_action`, then
resumes the same values after approval without asking the model to rewrite them.

For a direct owner question, status update, handoff, or coordination request,
the model must prefer `reply` whenever it can send a safe and useful response.
That response may acknowledge receipt, state verified current facts, identify
unknown dependencies, and describe the next coordination boundary without
inventing a completion promise or personal commitment. Remaining owner work is
not by itself a reason to replace the sender-facing reply with `notify`.
Incomplete facts are not by themselves a reason to replace the sender-facing
reply with `notify`; the reply should truthfully state what is known, what is
unknown, and which point needs owner confirmation. `notify` is reserved for
owner-relevant messages that do not directly mention the owner, or for direct
mentions where a sender-facing reply would expose sensitive private context.
`request_approval` holds an exact commitment or risky response that needs the
owner's approval. Shell output may locate evidence but is not itself a citable
source; before replying from a shell-discovered file, the model reads that file
through `read_workspace` to obtain a digest-backed source reference.

Availability checks and simple greetings from the configured owner to the
assistant bot, including "在吗", are fast-path work. The bot replies immediately
with bot identity without requiring Lark conversation history or a model call.

Lark conversation history is optional enrichment for an already-received work
item. If that bounded history cannot be loaded, the context bundle records an
incomplete selection with a non-secret reason and continues with the current
message. A history lookup failure must not make the working reaction disappear
and leave the owner request silently waiting for retry.

User-identity Lark calls use the current Keychain credentials. If a cached
user_access_token is rejected as expired, the client serializes recovery,
reloads any newer Keychain token, and otherwise uses the current refresh_token
through the official SDK. A successful refresh rotates both tokens in Keychain
before replaying the original request exactly once. Refresh failure remains a
typed authorization error; the client never loops indefinitely or logs token
values.

## User-Visible Modes

- `auto`: the default mode. The agent may autonomously ignore, record, notify,
  request approval, or reply as the owner when all policy gates pass.
- `approval`: the agent can prepare a reply, but sends the exact draft only
  after the owner approves it; rejection cancels the pending action.
- `paused`: the agent stops claiming new work and cancels pending unsent
  replies. Completed actions are kept for audit.

Mode switches are available from the local CLI. Owner-only bot control messages
are part of the product contract for the Lark adapter, but are not exposed until
the live bot callback adapter is configured.

## macOS User Service

On macOS, the agent can be installed as a current-user LaunchAgent with a menu
bar controller. The installed service uses label `com.liuchong.lark-agent` and
runs the installed `lark-agent daemon run` binary with explicit `--config` and
`--state` paths. The menu bar App is a thin controller: it shows LaunchAgent
status and invokes `daemon status/start/stop/restart` and `mode paused|auto`.

The user-level installation may write files only under the current user's
`~/.config/lark-agent`, `~/Applications`,
`~/Library/Application Support/lark-agent`, `~/Library/LaunchAgents`, and
`~/Library/Logs/lark-agent`. It must not require administrator privileges and
must not install into system LaunchDaemons.
Secrets must not be embedded in the plist or App bundle. If model environment
variables are persisted for launchd, they must be written only to a user-private
configuration file with restrictive permissions.

An intentional Stop, Restart, uninstall, or upgrade records an offline
transition and asks the bot to privately notify the owner before unloading the
LaunchAgent. A notification failure is audited and returned by the control
command but does not trap the service in an un-stoppable state. A crash cannot
send or fabricate an intentional-offline notice.

After configuration, state opening, interrupted-work snapshotting, intake
session creation, and scheduler configuration succeed, every new process
session sends one idempotent bot private message that the agent is online before
workers may claim new work. This includes
manual start, restart, upgrade, and successful recovery after an unexpected
crash. The notice reports interrupted and uncertain-action counts and states
that they were not replayed automatically. Lifecycle idempotency keys must be
stable for one transition, distinct between online and offline transitions, and
no longer than the public Lark message API's 50-character `uuid` limit. The
controller derives a 128-bit digest from the durable transition/session ID
instead of appending the unbounded ID to the API field.

Given a normal 40-character online session ID, when the ready transition sends
its private owner notice, then the request uses a stable `uuid` of at most 50
characters, the notice succeeds once, and launchd does not restart the daemon
because of message-field validation.

## Clean Installation

The standalone defaults are `~/.config/lark-agent/config.yaml` for config,
`~/Library/Application Support/lark-agent/state.db` for SQLite, and
`~/Library/Logs/lark-agent` for logs. The agent does not read old command-line
tool directories and does not migrate historical data.

The macOS installer runs SDK/Keychain doctor and only then loads the new
LaunchAgent. If the new daemon does not create a distinct ready session, the
installer unloads it.

For an upgrade, the installer builds and validates a candidate binary without
overwriting the currently installed executable and holds a per-user install
lock through the complete operation. It validates the current independent
configuration and builds the status application before stopping a loaded
standalone LaunchAgent. After
the existing installation accepts the stop request, the installer waits for a
bounded period until launchd reports the label absent; a still-visible label is
an in-progress stop, not an immediate failure. After the label is absent, the
candidate backs up the complete installation and temporarily removes the
unloaded plist so normal controls cannot restart it during replacement. It then
performs full doctor checks before atomically replacing the executable. A failed
stop aborts replacement; any later failure restores config, state, executable,
wrapper, private environment, status app, plist,
and the previously loaded service instead of leaving a mixed installation or
issuing a duplicate `launchctl bootstrap`.

Given an already loaded standalone service, when the installer is run again,
then the existing service is stopped before its executable or state is
replaced, all candidate checks pass before executable replacement, the new
service creates a distinct ready session, and the installer returns
success rather than `load launch agent`. Given another installer already holds
the install lock, a concurrent attempt fails before it can stop or replace the
service.

## Workspace Boundary

The configuration must contain exactly one absolute `workspace.root`. Local
rules, local file search, workspace memory references, and future local file
tools are limited to the real path of this root directory.

The boundary is enforced by path resolution, not by prompt text. Relative
paths, `..`, absolute paths outside the root, and symlinks escaping the root are
rejected before any model call or file read. Rules are loaded only from
`AGENTS.md` and `.agents/` inside the workspace.

Workspace directory, rule, and skill discovery is deterministic and bounded.
The first snapshot lists at most three directory levels and reports when entry
or byte limits truncate the result. Skills are listed by metadata and loaded in
full only when the model asks for one. Rules applicable to a discovered target
path are loaded on demand and may never traverse above the configured real
workspace root.

The shell tool accepts general commands but every command is executed by an
OS-enforced sandbox. On macOS the backend is Seatbelt through
`/usr/bin/sandbox-exec`. The process may read and write the workspace, read the
minimum system and toolchain paths needed to execute, and use a private runtime
directory inside the workspace. It may not read or write user files outside the
workspace, follow a symlink escape, access excluded secret paths, inherit model
or Lark credentials, or survive as a detached process after the tool call.
Failure to establish or verify the sandbox disables shell execution; there is
no unsandboxed fallback.

Shell approval is configurable and disabled by default. When enabled, risky
workspace mutations and external side effects wait in durable approval state
until the owner approves or rejects them. Approval configuration never disables
the workspace boundary or secret filtering.

Shell is not the primary code-search interface. Recursive or broad shell search
requests such as `grep -r`, `find` over an unconstrained tree, or `rg`/`grep`
without a bounded target directory are denied with a structured tool-policy
result that tells the model to use `search_workspace`, `search_code_symbols`,
`trace_code_path`, or `explore_workspace`. A shell command that is read-only in
Unix terms can still be denied when it would consume unbounded workspace or
turn budget.

## Coding Assistance

When a Lark message asks a software engineering question, the runtime treats it
as a coding question before the model chooses a terminal action. A coding
question is not answered by broad chat context alone. The model must either
state that the question can be answered from the current message, or create a
short investigation plan that names the code entry points, symbols, tools, and
stopping condition it intends to use.

The coding investigation is read-only by default. Plan-mode coding work may
write only the active plan artifact; production code edits, shell writes,
commits, pushes, deployments, and direct Lark sends remain denied or routed
through durable approval. One-off technical questions remain `CodingQuestion`
work. Only questions that need multi-turn follow-up with explicit completion
and blocking conditions become `CodingGoal` work.

`CodingQuestion` is foreground work with a finite reply budget. It may inspect
code, summarize evidence, ask for a missing interface or path, and reply. It
must not monopolize the foreground lane when it cannot converge. `CodingGoal`
is background work with persisted completion and blocking conditions. A new
owner fast-path or simple request must be claimable while a coding goal is
active.

Evidence must be structured. Workspace files and code-index results are
preferred over shell output. Shell output may locate files, but durable replies
must cite digest-backed workspace reads or code-index references. Each tool
result used in a coding conclusion has a receipt-equivalent audit record that
binds the run, tool call, argument hash, result digest, and source references.
The model may not claim it read, searched, tested, or verified something when no
matching receipt exists.

Tool and context budgets must fail soft for coding investigations. When a run
approaches the total tool-output budget, context budget, or model-turn budget,
the runtime summarizes old evidence, drops obsolete raw tool output from the
model-visible context, and asks the model to converge. It must not discard the
whole work item solely because a bounded investigation produced too much raw
output. Repeated Lark-context reads that return no new target-message context
are treated as no-progress tool calls and force convergence.

Before a coding reply is sent, a verify gate checks three properties:
completeness (the answer addresses the original question), correctness (the
claim is supported by cited code evidence), and coherence (the response obeys
this behavior contract and current tool-permission policy). A failed verify
gate blocks the group reply and either asks the model to repair the decision or
moves the exact draft to approval if the remaining issue is a risky commitment.

## Event Intake

The runtime accepts events normalized from these Lark sources:

- user-token polling for group and private messages visible to the owner.
- bot-visible `im.message.receive_v1` real-time events for owner private
  messages and owner-authored group mentions of the assistant.

Adapter code must normalize sources into a single event shape and dedupe by
message ID. Every observed event receives a durable intake receipt before it
can become work. The receipt records the online session, source, event time,
admission decision, reason, and linked work item. A daemon restart therefore
knows whether a message was admitted, completed, interrupted, duplicated, or
suppressed as offline backlog instead of guessing from its age.

Real-time intake is the primary low-latency path for requests that the owner
addresses to the assistant. It classifies and persists an event before workers
may claim it. The user-token poll remains active as a fallback and for messages
that are not bot-visible. If both adapters observe one message, its message ID
admits one work item and therefore one working reaction, one model run, and one
reply. Every later observation appends a duplicate receipt linked to that work
item without changing a terminal status. A real-time connection failure is
reported and retried with bounded backoff without stopping polling or queued
work.

Each successful daemon start creates an online session before intake begins.
An event created before that session boundary is offline backlog, not live work.
If no receipt exists, the runtime stores it as `offline_backlog` without
creating a claimable work item; if a receipt already exists, normal message-ID
deduplication preserves its recorded outcome. Events created during the current
session remain eligible even when transport delivery is delayed. This is a
session-boundary rule, not an arbitrary message-age timeout.

Before connecting, the adapter verifies that the published app version contains
`im.message.receive_v1` and bot scopes `im:message.p2p_msg:readonly` and
`im:message.group_at_msg:readonly`. A missing event or scope is a reported
real-time intake failure; it does not disable the user-token fallback.

Live polling and queue processing are independent. If a poll cycle fails
because Lark search or hydration is temporarily unavailable, the daemon still
attempts to claim already queued work during that tick. Poll failures are
recorded as intake health, not as a reason to starve the scheduler.

Assistant private-chat recognition may use configured assistant open IDs,
native mentions, explicit text prefixes, and discovered chat names. It must not
depend solely on a successful `SearchChats` name lookup. When metadata is
missing from a search result, hydration fills sender, message type, thread, and
create-time fields even if content was already present, then batch chat lookup
fills chat mode and `p2p_target_id`. The private partner identity is preserved
through the normalized event and matched against configured assistant open IDs.
If required chat metadata cannot be fetched, the poll fails without advancing
its cursor or persisting an unclassifiable private message, so a later cycle can
retry without permanently deduping the request as ignored.

Polling queries overlap the current session cursor by a bounded index-lookback
window so a message that Lark indexes late in the same session is still
ingested. Message-ID deduplication makes the overlap safe. Every new online
session advances the intake floor to its own start boundary, so polling cannot
turn offline backlog into an unsolicited reply after restart.

If user-token polling was unavailable, messages from that outage were never
observed by the local intake layer and therefore cannot be recovered with
`queue resume`. The owner may explicitly run `queue backfill` with a chat query
or exact chat ID and a bounded time range. Backfill only searches owner-mention
messages visible to the user token, records matching messages through the same
intake, routing, deduplication, and audit path as polling, and never advances
the normal poll cursor. It is an operator recovery action, not automatic history
replay.

## Conversation Context

Every normalized message preserves Lark's direct-parent, root-message, and
thread identifiers. Real-time intake and user-token polling must produce the
same relation metadata before message-ID deduplication chooses the durable work
item.

Context is always confined to the current chat. Without an explicit reply, the
resolver supplies a bounded, chronological window of messages created no later
than the target message; it never imports another group/private chat or messages
that arrived while delayed work was executing. The model determines semantic
relevance from that bounded nearby window without a separate context-selection
model call.

When the target replies to another message, the direct parent is authoritative.
The resolver follows the parent/root chain and, when the referenced message
belongs to a thread, reads the thread from its root through the target message.
The initial context builder and `get_lark_context` use the same resolver and
selection metadata. A model-visible selection records adjacent/reply-chain/thread
mode, the anchor message, truncation, and missing relation IDs.

The model-visible conversation remains bounded to 30 messages. Compaction pins
the root, directly referenced message, and target message before retaining the
nearest chronological messages. Missing/forbidden relation messages or a
partially readable thread produce an explicit incomplete-context marker and a
same-chat adjacent fallback; they never authorize a guessed antecedent. Older
durable work items may hydrate relation metadata by message ID, but completed
messages are not replayed solely to backfill context.

When an owner sends equivalent requests through private chat and group mention
inside a short dedupe window, intake links them to one canonical work item or a
completed result. The duplicate item must not start a second long
investigation.

## Queue Recovery

Work items must not be lost when a model call, context build, or reply send
fails. A failed item is stored as `retry_wait` with the failure reason, retry
count, next attempt time, and owning online session. It may retry with bounded
backoff only while that same daemon session remains online.

At startup, every non-terminal item owned by an earlier session is changed to
`interrupted`, not `received`. Recovery stores the latest durable model step,
tool call, decision, reply action, and owner-notification action as an
interruption snapshot. Received-but-unclaimed and retry-waiting work from the
earlier session is also paused, so restart cannot create a delayed unsolicited
reply. A missing lease timestamp is evidence of interruption, not permission to
run automatically.

Leases are work-kind specific and are refreshed while long runs make progress.
Fast-path and simple work have short leases. Coding questions and background
goals may have longer leases, but the lease must be compatible with their
configured loop timeout and must not leave the queue looking permanently stuck.
If the process dies and no heartbeat updates the lease, recovery marks the
latest run abandoned, snapshots the last durable stage, and pauses the work
item. The next session can inspect it but cannot claim it until the owner
explicitly resumes that exact item.
Every claim receives a unique lease token. Heartbeat, retry, completion, Goal
creation, and sender/owner side effects validate that exact token; an expired
worker cannot mutate or send for a newer claim.

The scheduler owns claim order. It must always prefer completed-reply owner
notices, pending approvals that can resume, and fast owner requests over
foreground coding questions, and it must prefer foreground work over background
goals. A long model loop or shell command in one lane must not prevent the
daemon from claiming eligible work in a higher-priority lane when another
worker slot is available.

All foreground workers share one serialized SQLite connection inside the daemon.
If a separate operator process briefly holds the SQLite write lock, durable
worker transitions wait for a bounded interval and continue after the lock is
released. A write that remains blocked beyond that interval fails explicitly;
the agent must not report or imply that the transition succeeded.

Operators use `queue inspect --work-id <id>` or
`queue inspect --message-id <id>` to
see whether a message was suppressed, queued, running, interrupted, replied, or
completed, including the exact last durable stage and any uncertain external
action. `queue resume` is the only normal path that makes interrupted or
offline-backlog work claimable in a later session. Replaying an already terminal
item requires an additional explicit force flag.
Messages that were never observed because user-token polling was not configured
have no queue receipt; for those, operators use `queue backfill --chat-query
<query> --since <time> --until <time>` to create bounded intake records first.
`queue retry` has a narrower purpose: it may only accelerate ordinary
`retry_wait` work owned by the currently active session and having no executing
or blocked external action. It must never make prior-session, processing,
interrupted, terminal, or uncertain-action work claimable.

Each model run, assistant message, tool invocation, tool result, final
decision, and external action is durably recorded. On explicit resume, old
read-only evidence is audit history rather than current truth. Interrupted
read-only tools may be selected again by a new run. An interrupted shell or IM
send is marked uncertain and is never blindly repeated; recovery first
reconciles observable state. If it cannot prove whether the side effect
happened, the item remains paused until the owner resolves that uncertainty.

The default multi-step investigation budget is 150 model turns with a two-hour
elapsed-time ceiling. Model turns and persisted audit steps are different:
model and tool-result steps are both recorded, so one model turn can produce
multiple audit steps. Operators may configure up to 300 model turns, but the
elapsed-time, repeated-call, context, tool-output, and shell-time limits remain
independent finite boundaries. The initial system prompt states the configured
model-turn ceiling, and every model request includes its current one-based turn,
total turn budget, and remaining turns so the model can plan and converge before
the runtime limit is reached.

Provider rate limits honor `Retry-After` when present and otherwise use bounded
exponential backoff from 15 seconds to 15 minutes. A configurable retry ceiling
moves permanently failing work to dead letter instead of retrying forever.

Runs carry model and agent-configuration fingerprints for diagnosis. A changed
runtime may report which failed, ignored, dead-letter, or legacy items were
produced by an older contract, but it never requeues them automatically.
Re-evaluation after an upgrade is an explicit owner action through
`queue resume`; completed outward replies remain protected by the same
idempotency and reconciliation rules.

## Routing

The router first applies deterministic gates: pause state, duplicate events,
blacklists, message age, unsupported message type, and loop prevention. Semantic
judgment is used only after these gates.

Messages that explicitly mention the owner must become work items. Messages
without an explicit mention can become work items when deterministic routing and
the model judge them related to the owner using user profile, role, tasks,
projects, memory, rules, message history, and workspace search results. The
router may see all user-visible conversations, but only candidates that pass
hard gates and relevance checks enter the model.

The owner can also initiate the assistant directly. A message sent by the
configured owner becomes an owner-request work item when it either mentions a
configured assistant bot identity/name in any conversation, or appears in a
private chat whose partner open ID or discovered name matches the configured
assistant. This path is owner-only: the same bot mention or private chat from
any other sender is ignored before the model sees it. Owner-request replies are answers to the
owner's own prompt, so the pre-send "owner already replied" cancellation check
does not treat the original prompt as a solved thread.

The router attaches a work kind and priority to every accepted item. Owner
assistant requests that match a fast-path command are `fast_path` work. Owner
assistant requests that need a short answer but no code evidence are
`simple_question` work. Engineering requests that require code evidence are
`coding_question` work unless they explicitly need durable follow-up, in which
case they become `coding_goal` work with persisted completion and blocking
conditions. Non-owner assistant private messages and bot mentions remain
ignored before any model call.

## Reply Policy

Before replying as the owner, the runtime must:

1. build a source-backed draft;
2. check risk and policy gates;
3. wait for the configured owner-response window;
4. re-read or re-check the thread state;
5. cancel if the owner already replied, the message was withdrawn, or the
   question was solved;
6. send with a stable idempotency key;
7. notify the owner through the bot with the reason, sources, and rollback
   options.

Reply decisions must include non-empty model-authored text. There is no generic
acknowledgement fallback. `notify` performs a real owner notification,
`request_approval` enters a durable actionable state, `record` persists an
auditable trajectory, and ignore/reply outcomes preserve the actual action
status rather than treating blocked or awaiting actions as completed.

`--dry-run` uses the same intake, context, and model decision path but does not
execute the reply tool. Initial live validation should run dry-run across
visible conversations, then allow one bounded configured test-chat reply. The
current live acceptance chat is `Test Group`; Example Group is excluded from live testing.

The model is not given tools for payments, contracts, personnel decisions,
permission grants, permanent external deletion, subjective commitments,
arbitrary paths outside the workspace, or arbitrary Lark OpenAPI calls. The
workspace shell remains constrained by Seatbelt, exact action audit, secret
exclusions, bounded output/time, and optional approval.

## Memory

Memory has four layers:

- immutable trajectory: event, decision, tool result, and audit records;
- episodic memory: past similar events and owner follow-up behavior;
- semantic memory: confirmed profile, role, project, and preference facts;
- procedural memory: owner-approved rules and local workspace rules.

Chat messages and documents are untrusted data. They cannot promote themselves
into procedural rules or expand local or Lark permissions.

Trajectory storage is append-only and subject to configured retention.
Credentials, authorization headers, and unsanitized process environments are
never stored. Large tool output is bounded for the model and stored only after
secret filtering, with digest and truncation metadata.

## Verification Requirements

Every behavior change must have an integration test. Required regression areas:

- workspace path escape through `..`, absolute paths, and symlinks;
- duplicate real-time and polling events;
- owner already replied before the waiting window expires;
- prompt injection attempting to change policy or read secrets;
- mode switches across `auto`, `approval`, and `paused`;
- token or scope failures for user-identity replies;
- daemon restart and retry behavior.

The multi-step loop is accepted by these executable BDD scenarios:

- Given owner asks `@assistant 几点了`, when routing runs, then the work item is
  classified as fast-path owner work, does not start a model run, and replies
  through the normal idempotent reply runtime.
- Given an owner request enters processing in assistant private chat or through
  an owner-authored assistant mention, when routing accepts it, then the bot
  adds the keyboard working reaction before work starts and removes it only
  after a terminal outcome, without a timer controlling removal.
- Given another sender mentions the owner, when the agent evaluates or replies
  as the owner's delegate, then it never adds the assistant working reaction.
- Given an owner request is visible through `im.message.receive_v1`, when the
  real-time adapter receives it, then it is classified and persisted immediately,
  without waiting for the next user-token poll.
- Given the real-time adapter and the user-token poll observe the same message,
  when both attempt to persist it, then message-ID deduplication admits one
  work item and only one working-reaction/reply lifecycle.
- Given the real-time connection fails, when the daemon remains live, then it
  retries the real-time adapter with bounded backoff while polling and queued
  work continue.
- Given the published app lacks the private-message or group-mention bot scope,
  when real-time intake starts, then it reports the exact missing scope and
  leaves user-token polling active.
- Given a reply decision reaches execution without a configured reply handler,
  when the daemon finishes the decision, then it retries instead of completing
  a reply that was never sent.
- Given a poll cycle fails while the queue already has received work, when the
  daemon tick continues, then scheduler claiming still runs and the queued item
  can be processed.
- Given owner asks the same coding question in private chat and in a group
  mention inside the dedupe window, when both events are ingested, then only
  one canonical investigation runs and the duplicate item links to that result.
- Given a foreground coding investigation is active, when owner sends a new
  fast-path request, then the scheduler processes the fast-path item before the
  background or lower-priority coding work.
- Given two foreground workers are ready to persist agent runs while another
  process briefly holds the SQLite write lock, when that lock is released within
  the configured wait interval, then both run starts persist without a
  `database is locked` failure.
- Given a running coding question updates its heartbeat, when recovery scans
  leases in the same online session, then the item remains owned; given the
  process dies, when the next session starts, then recovery abandons the run,
  snapshots its last durable stage, and pauses the item as `interrupted`.
- Given the model requests `grep -r`, unconstrained `find`, or `rg` without a
  bounded target directory, when the shell tool policy evaluates the call, then
  it denies the shell call and instructs the model to use bounded code-search
  tools.
- Given a private assistant message becomes searchable only after the polling
  cursor has advanced, when the next overlapping poll runs, then message-ID
  deduplication admits it once without reaching messages before cold start.
- Given a private assistant chat search result has content but lacks sender or
  chat-type metadata, when hydration runs, then sender, chat type, message type,
  thread, create time, and private chat partner are filled from message and
  batch chat metadata before routing.
- Given required batch chat metadata is unavailable, when polling encounters an
  unclassifiable message, then it does not advance the cursor or persist that
  message as ignored, and a later cycle can retry it.
- Given an owner request does not reply to another message, when conversation
  context is built, then it contains only bounded nearby messages from the same
  chat at or before the target message in chronological order.
- Given messages arrive after a delayed target was queued, when that target is
  processed, then those future messages are excluded from its context.
- Given an owner request directly replies to a message, when context is built,
  then the direct parent and its bounded parent/root chain are pinned as the
  authoritative antecedent instead of unrelated nearby chat messages.
- Given the quoted message belongs to a thread, when context is built, then the
  root and thread messages through the target are included in chronological
  order and later thread messages are excluded.
- Given a reply chain or thread exceeds the model-visible bound, when context is
  compacted, then root, direct parent, and target remain present and the
  selection reports truncation.
- Given a quoted parent cannot be read, when context resolution completes, then
  it returns same-chat adjacent context with an explicit incomplete marker and
  the model does not guess the missing quoted content.
- Given real-time intake and polling observe the same replied message, when
  either path wins message-ID deduplication, then `reply_to`, `root_id`, and
  `thread_id` have the same meaning and produce the same context selection.
- Given a semantically similar message exists in another chat, when nearby or
  quoted context is resolved, then that other chat is never read or included.
- Given doctor or queue summary is requested, when it reads state, then it
  reports lane counts, stale work, recent latency, model turns, tool calls, and
  fast-path hits without exposing secrets.
- Given a message answerable from initial context, when the first model turn
  runs, then it calls `submit_decision` without an unnecessary information
  tool.
- Given a code question without enough initial evidence, when the loop runs,
  then the model chooses searches and reads, may revise an unsuccessful query,
  and submits a decision whose source references were produced by those tools.
- Given a coding question needs code investigation, when the model starts the
  run, then it creates a bounded investigation plan before broad workspace
  search and the plan names entry points, symbols, tools, and stop conditions.
- Given a coding investigation is in plan mode, when the model attempts to edit
  production code, run a write shell command, commit, push, deploy, or send
  Lark messages directly, then the runtime denies that operation until the plan
  exits through the configured approval path.
- Given a coding question only needs a one-off technical answer, when it is
  processed, then it remains `CodingQuestion` work; given the question requires
  multi-turn follow-up with explicit completion and blocking conditions, then it
  becomes `CodingGoal` work with persisted status and budget.
- Given a tool result is cited in a coding conclusion, when `submit_decision`
  is validated, then a matching receipt-equivalent audit record must bind the
  run, tool call, arguments, result digest, and source references.
- Given the coding run approaches tool-output, context, or turn budget, when
  useful evidence already exists, then the runtime summarizes stale evidence and
  forces convergence instead of failing the whole work item only because raw
  output exceeded a bound.
- Given repeated `get_lark_context` calls return no new target-message context,
  when the model asks again, then the runtime rejects the no-progress call and
  requires a decision or a different evidence tool.
- Given a coding reply is ready to send, when the verify gate checks it, then
  the reply is sent only if it addresses the original question, is supported by
  cited code evidence, and obeys current policy; otherwise the draft is repaired
  or held for approval.
- Given the default agent configuration, when a deep investigation starts,
  then it has 150 model turns and a two-hour ceiling; given an operator selects
  a custom budget, then values through 300 are accepted and larger values fail
  validation.
- Given a configured model-turn budget, when the initial and subsequent model
  requests are built, then the system instructions derive the total, current,
  and remaining turns from that runtime value rather than a duplicated literal.
- Given a human message directly mentions the owner with a status update,
  coordination request, commitment, or follow-up, when the model triages it,
  then it treats the message as addressed to the owner workflow and chooses
  among record, notify, reply, or approval instead of dismissing it as “not
  addressed to the assistant”.
- Given the configured owner privately messages the assistant chat, when the
  message is polled, then it enters the model as an owner-request work item and
  the assistant replies with bot identity without a redundant owner notice.
- Given the configured owner privately asks the assistant "在吗", when the item
  is processed, then the bot replies "在的。" as fast-path work and removes the
  working reaction without loading conversation history or calling a model.
- Given the configured owner mentions the assistant in a group and asks "在吗",
  when the item is processed, then the bot replies "在的。" through the same
  fast path without loading conversation history or calling a model.
- Given a non-fast-path owner request has already been received and bounded Lark
  history loading fails, when context is built, then the current message still
  reaches the model and the bundle marks its context selection incomplete.
- Given a user-identity Lark request rejects the cached access token as expired
  and Keychain contains a newer token, when the request recovers, then it reloads
  and replays once without consuming the refresh token.
- Given no newer access token exists but a refresh token is available, when the
  request recovers, then the official SDK rotates both tokens, persists them,
  and replays the original request once.
- Given the configured owner mentions the assistant bot in any conversation,
  when the message is polled, then it enters the model as an owner-request work
  item even when the owner did not mention themselves, and a reply uses bot
  identity.
- Given any non-owner privately messages or mentions the assistant bot, when
  routing runs, then the runtime ignores it before any model call.
- Given an owner-request reply is ready to send, when the pre-send thread check
  runs, then the original owner prompt is not treated as an existing owner
  answer that cancels the reply.
- Given app/bot messages appear in Lark context, when the model determines its
  identity, then those messages remain evidence and do not turn the personal
  assistant into that bot persona.
- Given an older runtime ignored a direct owner mention after read-only evidence
  gathering but without sending a reply, when the operating-contract
  fingerprint changes, then the upgraded daemon reports that older item but
  leaves it unchanged until the owner explicitly resumes it.
- Given a direct technical question and code evidence that supports useful facts
  plus explicit unknowns, when the model chooses a terminal action, then it
  replies to the original message instead of notifying the owner merely because
  the answer is not exhaustive.
- Given a direct status update, task handoff, or coordination request and the
  model can safely acknowledge it with current facts and dependency boundaries,
  when it chooses a terminal action, then it replies to the sender instead of
  privately notifying the owner merely because owner work remains.
- Given a direct owner question and the model has incomplete factual evidence,
  when it chooses a terminal action, then it still sends a truthful
  sender-facing reply naming the unknowns and owner confirmation needed instead
  of finishing as `notify`.
- Given an older reply policy held a low-risk direct owner mention in approval,
  when the upgraded daemon starts, then it preserves the exact approval and
  does not send or requeue it without an explicit owner action.
- Given a direct coordination request produces a successful group reply and
  still leaves owner work, when reply execution completes, then the sender sees
  the reply first and the owner then receives a private notice that states the
  agent replied and summarizes the remaining action from `owner_action`, not
  from an internal routing or classifier reason.
- Given Lark returns mention placeholders such as `@_user_2` with a `mentions`
  mapping, when the agent builds context and sends a reply, then the mapping is
  retained and sender-facing text never leaks the internal placeholder. Known
  placeholders are rendered as Lark mentions or readable names; unknown
  placeholders block the reply instead of being sent.
- Given the agent replies as the owner on the owner's behalf, when the message
  is posted to Lark, then the visible reply text starts with the `🤖` robot
  marker exactly once.
- Given the assistant bot replies to an owner-request private message or
  owner-authored group bot mention, when the message is posted to Lark, then it
  uses bot identity and does not add the owner-delegation `🤖` marker.
- Given the group reply is blocked, cancelled, awaiting approval, or fails, when
  reply execution stops, then the owner notice must not claim that the agent
  already replied.
- Given the group reply succeeds and the following private owner notice fails,
  when the work item retries, then it resumes the persisted owner notice with
  the same idempotency key without rerunning the model or group reply.
- Given a proposed response would make an unverified personal commitment, when
  the model cannot remove that commitment while preserving a useful reply, then
  it requests approval with the exact response and owner follow-up, and approval
  resumes those persisted values rather than rerunning the model.
- Given shell search locates relevant code, when the model prepares a
  source-backed reply, then it reads the relevant file with `read_workspace`
  and cites that digest-backed source rather than treating raw shell output as a
  citation.
- Given the provider constructs a terminal decision, when it fills `risk`, then
  the native tool schema permits only `low`, `medium`, `high`, or `forbidden`
  and keeps explanatory prose in `reason`.
- Given an older operating contract completed a direct mention as `notify`
  without sending a reply, when the contract fingerprint changes, then the
  upgraded daemon reports it as eligible for explicit re-evaluation but does
  not automatically requeue it.
- Given untrusted content requests a workspace escape or credential, when a
  read or shell tool runs, then code and the OS sandbox reject it and return a
  structured tool error to the model.
- Given shell approval is disabled, when a workspace command runs, then it
  executes and is audited; given approval is enabled and the command is risky,
  then it waits durably and resumes only after owner approval.
- Given the daemon crashes during a read-only tool, when it restarts, then the
  work item is interrupted at that tool stage and remains paused until explicit
  resume; given it crashes during shell or IM execution, then the action
  becomes uncertain and is not automatically repeated.
- Given retrying or dead-letter work whose latest failed run used an older model
  or agent configuration, when the upgraded daemon starts, then it is reported
  but not requeued; a completed reply is not re-evaluated.
- Given an event was created before the current online session and has no prior
  intake receipt, when real-time reconnect or polling observes it, then the
  runtime records `offline_backlog` and creates no claimable work item.
- Given an event was created during the current online session but is delivered
  late, when intake observes it, then the session boundary admits it normally;
  no arbitrary age cutoff suppresses it.
- Given the daemon starts SDK WebSocket real-time intake, when no event is
  currently available, then the connection remains supervised without depending
  on a subprocess stdin lifecycle; when the daemon stops, context cancellation
  stops intake without replaying already observed events.
- Given a prior-session work item was received, retrying, processing, or
  executing, when the daemon starts, then it becomes `interrupted` with its last
  durable model/tool/action stage and workers cannot claim it.
- Given the owner runs `queue inspect` for a message, when state is returned,
  then it states whether the message was observed, admitted, replied,
  interrupted, or suppressed and includes any uncertain external action.
- Given the owner explicitly resumes one interrupted or offline-backlog
  message, when the current session claims it, then a new run uses current
  evidence while preserving the prior audit timeline.
- Given user-token polling was unavailable and the owner later authorizes it,
  when the owner runs `queue backfill` with a bounded chat and time range, then
  only matching @Owner messages are recorded through normal intake and the
  normal poll cursor is not advanced.
- Given a reply send was in flight when the process stopped, when recovery
  cannot prove whether Lark accepted it, then the action remains uncertain and
  no reply or owner notice is resent automatically.
- Given the user intentionally stops or restarts the service, when control
  begins, then the bot sends one idempotent private offline notice before
  `launchctl` unloads it; an unexpected crash sends no false offline notice.
- Given configuration, recovery snapshot, intake creation, and
  workers are ready, when any new daemon process session comes online, then the
  bot sends one idempotent private online notice with interrupted and uncertain
  counts, including after crash recovery.
- Given a reply decision, when policy blocks, awaits approval, sends, or finds
  the owner already replied, then the durable action status records that exact
  outcome and no generic fallback text is sent.
