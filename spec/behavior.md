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

The runtime may also bridge a trusted GitHub workflow notification into Lark.
A short-lived GitHub Action sends one bot-authored notification through the
HTTP API and exits. The installed daemon remains the only WebSocket consumer.
The Action requires an explicit Lark OpenAPI base URL and never relies on the
official SDK's Feishu default for an international Lark installation.
When a human replies to that notification, the daemon verifies a versioned
GitHub reference from the quoted current-app message and may expose fresh,
bounded, read-only GitHub evidence to the model. It never treats a human or
another app's marker as trusted control data.

## Harness Architecture

The runtime is split into explicit layers so a slow or divergent model run
cannot hold the whole Lark assistant hostage:

- Intake normalizes user-token polling and bot-visible real-time events into
  the same durable event shape, dedupes by message ID, and stores the event
  before any model call or reply side effect.
- Router and fast path are deterministic. They classify group assistant
  requests, private owner requests, direct owner mentions, simple local
  questions, coding questions, and long-running coding goals before a model run
  is selected.
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
  snapshots stale work at its last durable stage before startup convergence.
  Safe work is explicitly reassigned to the new ready session; uncertain
  external actions are terminalized and never replayed.

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
overview, a bounded project catalog, workspace rule paths, skill summaries,
the current Lark message, and recent conversation context. The directory
overview reaches at most five levels, 600 entries, 80 entries per directory,
and 16 KiB of serialized prompt text. The project catalog is derived from
bounded manifest and repository markers inside that same scan and is retained
before lower-value directory entries when the prompt is compacted. It does not
pre-guess code-search terms from the message. Code questions are investigated
by the model using search, read, rules, skills, Lark-context, local Git history,
and shell tools.

The loop has hard limits for model turns, elapsed time, individual and
cumulative tool output, and repeated no-progress model turns. Multiple tool
calls emitted by one model turn count as one no-progress opportunity, not one
opportunity per call. A useful successful call in that turn resets the
no-progress streak even when a sibling call is rejected by policy. Reaching a
limit never creates a guessed answer. The work item becomes retryable,
dead-lettered, or a source-backed notify/approval decision according to the
failure type.

Fast-path work does not enter this loop. A configured owner asking deterministic
local questions such as time, date, ping, daemon status, help, doctor, or queue
summary receives a local answer through the normal reply runtime without a
coding investigation. A simple non-coding owner question may use at most the
simple-agent budget and cannot call shell. The default simple-agent budget is
three model turns so an evidence-backed request can perform initial search,
read one narrowed production source, and then submit its conclusion. A coding
question has a separate foreground budget. Once a coding run has citable
workspace evidence, the model is told to answer immediately when that evidence
covers the requested fields instead of expanding into unrelated chat history,
call-site proof, or repository-wide searches. An exact function definition is
sufficient evidence for that function's direct return behavior unless the user
also asks whether it is reachable from a production entry point. When useful
evidence exists and the coding run reaches its final two turns, the runtime
normally reserves those turns for `submit_decision`: additional investigation
calls are rejected so the model can return a truthful answer, explicit unknown,
or clarification without exhausting the run and retrying the whole
investigation. A concrete serialized-shape request has one narrower exception.
If current-run reads still contain only an opaque declaration, the penultimate
turn exposes only one targeted `read_workspace` call so the model can read a
known current documentation example, fixture, protocol definition, or
serializer. The final turn then exposes only `submit_decision`. Broad search,
listing, shell, Lark-history, and unrelated code-index tools remain unavailable
during that reserved evidence-completion turn.

`submit_decision` is the only terminal model tool. It has no external side
effect. Its structured output is validated by Go and then passed to the normal
reply or notification policy. Lark reply and notification tools are never
registered as model-callable tools. The native tool schema constrains `risk` to
`low`, `medium`, `high`, or `forbidden`; explanatory text belongs in `reason`,
so an invalid risk sentence cannot turn a terminal decision into a failed tool
round trip.

Every sender-facing `reply` also declares `reply_outcome`:

- `complete` means every concrete requested claim meets the evidence contract
  for the current work kind;
- `partial` means bounded work produced one or more useful supported facts or a
  proven investigation limit while named facts remain unknown;
- `clarification` means an exact missing input or ambiguous referent prevents a
  safe answer.

The outcome is independent from `evidence_status`. A complete coding answer
normally requires `verified`. A partial answer may combine verified claims with
explicit unknowns or may be `insufficient` when the only safe result is that a
configured workspace, path, or symbol could not be verified. A clarification
does not assert a code fact and does not require an irrelevant successful code
read.

The decision carries structured `progress`: completed checks backed by
current-run receipts, an initial safe finding, exact unknowns, and one concrete
next step. The runtime validates this structure instead of relying on fixed
phrases such as "I checked". A claim that a read, search, test, or verification
happened still requires its matching receipt. Before quality validation, Go
replaces model-supplied `completed_checks` with successful tool names from the
current run. A coding `clarification` may skip code reads only when it uses
`evidence_status=insufficient`, names exact missing inputs, and allows the
runtime to replace free-form code prose with a deterministic unknown/next-step
request.

The model-visible contract has three layers. A stable core contains identity,
authority order, Lark and Workspace safety, untrusted-data handling, and the
rule that external sends are runtime-only. A task-process layer describes the
short flow for the current work kind: understand, choose direct answer,
clarification, or investigation, plan if required, gather minimum evidence,
classify claims, and submit a typed outcome. A dynamic run-state reminder is
generated by Go before every request and after compaction. It reports the
remaining turn/tool/context budgets and whether the run is in terminal-only
mode. Terminal repair state adds successful completed tool names, typed
unknowns, the last failed gate, and allowed terminal outcomes. A repeated
fingerprint writes its occurrence count and required disposition into that
failed-gate state. The model never supplies or maintains these counters.

Prompt instructions are not policy enforcement. Every tool, path, permission,
budget, evidence, and send restriction remains enforced by Go. Dynamic prompt
text and structured tool denials describe the same runtime policy so a weak
model receives an actionable next state without gaining authority.

The model has two explicit Lark roles. An `assistant_request` answers the
configured owner when that owner natively mentions the assistant in an allowed
group, using bot identity. A `direct_mention` acts on behalf of the configured
human owner when another human mentions that owner, using the delegated-reply
policy. An inbound human P2P message to the owner is a `private_message`
candidate and uses that same delegated-reply identity and policy only when the
semantic gate finds an outstanding conversational need for an owner response.
A private answer to an owner-initiated question, acknowledgement, reaction, or
ordinary continuation with no new request may be `no_reply_needed`. For ordinary
private messages, `unanswered` must be grounded in a new question, request,
invitation, or action obligation present in the target text itself; context may
explain that obligation, but must not invent it from the owner's earlier
question or from an informative product/design statement. A private
`owner_request` answers the configured owner's own assistant prompt using bot
identity.
Non-owner private messages addressed to the assistant and non-owner native
assistant mentions are ignored before model work. A direct owner mention is
addressed to this personal-assistant
workflow even when it is a status update, coordination request, commitment, or
follow-up rather than a grammatical question. Such a message may still need
evidence before choosing an outcome. `ignore` is for content with no
owner-relevant information or action. `record` preserves an owner-relevant
update that needs no interruption, `notify` surfaces an owner action or
coordination need, `reply` sends a source-backed response, and
`request_approval` holds an exact risky or uncertain action. App/bot messages
in conversation context are evidence only and never redefine the role selected
by the router.

Every allowed `direct_mention` and `private_message` first becomes durable
`waiting_user` work. It is not claimable until the trusted message creation or
latest edit time plus `policy.owner_wait`, whose default is three minutes.
Waiting does not hold a worker, lease, or completed reply draft.

At the deadline, a tool-free semantic resolver reads a paginated, bounded
same-chat window containing a short pre-target conversation-direction window,
the target, related pending targets, intervening discussion, and owner-authored
messages after the target. It evaluates each target independently. Reply/thread
relations, adjacency, and the mere presence of a later owner message are
evidence but do not prove that the owner answered the target. For an ordinary
private message that does not explicitly mention
the owner, the resolver also decides whether the target reasonably calls for a
response at all. A high-confidence semantic answer cancels only the matched
target; a high-confidence `no_reply_needed` result cancels a private answer,
acknowledgement, reaction, owner-led conversational continuation, or group
`@Owner` social acknowledgement that contains no explicit new action obligation,
without inventing another response. A high-confidence unanswered result admits
only that target to the delegated agent loop after it records a target intent
and an exact response-obligation quote from the target message. If the target is
classified as an answer, acknowledgement, continuation, social compliment, or
informative product/design statement and does not contain such an obligation,
the runtime normalizes it to `no_reply_needed` instead of starting an
investigation. Explicit group `@Owner` remains the required entry condition for
group delegated work, but the semantic gate may still suppress messages that
only acknowledge, compliment, react, or share information. Ambiguous, malformed,
low-confidence, truncated, or unavailable resolution fails closed and retries
after the configured semantic retry delay.

Immediately before semantic classification, before finishing durable
investigation, and before sending a held candidate, the resolver reads reactions
on the exact target with user identity. A reaction by the configured owner whose
emoji type is one of `Get`, `OK`, `DONE`, `THUMBSUP`, `CheckMark`, `Yes`, or
`LGTM` is deterministic evidence that the owner handled the target. The result
is `answered` with confidence `1`, no sender-facing reply is sent, and the
reaction evidence is recorded in the semantic audit row. Reactions by other
users, bot reactions such as `Typing`, unsupported emoji types, reactions on
other messages, or reactions that were later deleted do not count. If reaction
reading fails, is unauthorized, or cannot finish within its page bound, the
resolver fails closed and retries; absence of readable reaction data is never
permission to guess that the owner did not handle the target.

The semantic resolver and the main delegated Agent consume one logically
identical context snapshot. The snapshot includes at most twenty same-chat
messages and three minutes before the target, the target itself, messages
observed through the semantic cutoff, and explicitly related reply/root/thread
messages. It retains sender display names, message types, and typed attachment
descriptors. The semantic result also carries a concrete task summary, a
simple/investigation/coding task class, classification confidence, and whether
durable progress is required. A high-confidence contextual coding task receives
the coding budget and read-only workspace tools even when the target's literal
last sentence has no coding keyword.

Reply/root/thread relations are resolved even when the related message is older
than the adjacent chat page. If a required relation cannot be read, the
snapshot is incomplete and semantic classification fails closed rather than
guessing the antecedent.

When durable progress starts, the normalized context snapshot is persisted
without ephemeral image bytes. A restart restores the original cutoff, digest,
task classification, and normalized messages and skips initial
reclassification against a different time window. Restored image descriptors
are explicitly unreadable until fetched again; persisted metadata never claims
that discarded bytes remain available. The final owner-handled check still
reads fresh same-chat context immediately before any final reply.

The delegated agent context includes bounded post-target discussion so its
response reflects what happened during the grace period. The semantic result
is retained in the audit ledger. Immediately before a reply action is persisted, the runtime reads
messages newer than the prior semantic cutoff and re-runs semantic resolution
when owner content changed. A newly matched owner answer cancels the reply;
ambiguity delays it. Lark does not expose a compare-and-send primitive, so this
last read minimizes but cannot eliminate the interval before the send call.

`policy.reply_scope` independently selects all groups or configured groups for
`@Owner`. `policy.private_reply_scope` selects all inbound human P2P messages as
semantic candidates or disables that entry point. Existing allow/block chat
and user lists apply to both. Bot/app messages, owner-authored messages that do
not invoke the assistant, and non-owner messages to the assistant remain
outside delegated intake. Polling must discard an owner-authored non-assistant
message before durable work intake; later lexical relevance inference cannot
re-admit it.

A quoted app/bot message may carry a GitHub reference, but the reference is
usable only after code verifies that the message sender is the configured
current Lark application, the marker has a valid HMAC signature made with the
same Lark app secret, the repository is explicitly allowed, and the message is
in the target's same-chat reply/root chain. Marker-shaped text copied through
an ordinary bot answer remains untrusted. An invalid signature leaves the
GitHub tool unavailable but does not block an otherwise valid business answer. The
model cannot supply or change the repository, pull request, workflow run, API
base URL, or credential. A verified reference adds one read-only GitHub evidence
tool; it does not widen sender authority or change the invocation role.

Every model run derives tool authority from the durable sender identity. Work
triggered by anyone other than the configured owner is read-only: it may inspect
bounded same-chat context and workspace/code evidence needed for a business
answer, but it cannot execute shell, search other chats, modify or delete
workspace content, commit, deploy, send an arbitrary message, or invoke any
present or future side-effect or owner-only tool. The tool registry enforces
this boundary before an executor runs, and prompt content cannot widen it.

The assistant accepts business questions, not descriptive reconnaissance about
its host or work environment. Requests to inspect credentials, enumerate the
host, user home, processes, network, installed tools, or read an explicit local
path outside the configured workspace are refused before evidence tools run.
The refusal is concise and does not disclose the requested environment detail.

A successful delegated `reply` to another sender is a compound user-visible
outcome: before the sender-facing reply is sent, the bot durably records and
delivers a private owner notice containing the exact intended reply and any
remaining owner action. Only after that notice is confirmed does the
user-identity path send the intelligent-assistant reply to the original Lark
message. This ordering makes the sender-facing statement that the named owner
was notified truthful. A group message that natively mentions the assistant,
or a private owner request addressed to the assistant, is instead replied to
directly with bot identity and does not send a delegated owner notice. The
private notice describes the intended reply, not a reply that has already been
sent, so it remains truthful if the sender-facing reply later fails.
Standalone `notify` means no
sender-facing reply could safely be sent; it is not a substitute for this
delegated owner notice.

Reply approval preserves this invocation identity together with the exact
draft. Approving a held assistant request or private owner request still sends
that draft with bot identity and does not create a delegated owner notice.
Approving a delegated owner mention first delivers the owner notice and then
sends the exact draft with user identity. Approval may authorize the content and
commitment, but it never changes who the original request addressed.
When a reply action enters durable approval, the bot privately tells the owner
that the draft has not been sent. That notice includes the approval action ID,
the exact proposed reply, the remaining owner action, and exact private-chat
approve and reject commands. The approval ID is carried as typed reply-action
data rather than parsed from display text. Retrying the same approval uses a
stable notification idempotency key and does not produce duplicate notices.
The notice names the original role correctly: delegated work is a delegated
reply draft, while an assistant request or private owner request is an
assistant reply draft. A globally configured approval mode does not suppress
the delegated owner notice after approval: an approved delegated draft still
delivers that notice before the sender-facing reply.
For an approval written by an older version before relevance was embedded in
the action request, the daemon restores relevance from the work item's durable
decision and consumes the legacy exact-draft idempotency key. It never guesses
a sender identity from an absent or unknown relevance, and it completes the
legacy approval audit after the external reply succeeds.

Every terminal `reply` requires a configured reply executor. A missing executor
is a retryable runtime error, never permission to persist a successful reply
decision without an external message. Normal auto replies, not only approval
resumes, are recorded as durable idempotent reply actions before calling Lark
and completed with the returned message ID or exact error.

For requests addressed directly to the assistant, the bot adds the keyboard
working reaction to the source message after deterministic routing and before
fast-path or model work starts. Lark names this keyboard reaction `Typing` in
its API; it is a message reaction, not a timer or native typing-status signal.
This applies to the assistant private chat for the configured owner and to any
accepted group message that natively mentions the assistant bot. The reaction
is removed only after reply, ignore, approval, failure, or cancellation reaches
its terminal outcome; no fixed display delay controls the lifecycle. It is not
used when another sender mentions the owner and the agent is acting as the
owner's delegate. Reaction cleanup is audited and retried without resending an
already completed reply.

The delegated owner notice is a separate durable action with a stable Lark
idempotency key. New work completes that private notice before the
sender-facing reply starts. A known notice failure retries only the notice and
does not send the sender-facing reply. If a process interruption makes the
notice result uncertain, it is not replayed. Compatibility recovery treats an
older post-reply notice as finish-only work only when a completed durable reply
action proves that the sender-facing reply already succeeded.

The structured decision separates `reply_text` from `owner_action`.
`reply_text` is the exact sender-facing message. `owner_action` is the concise
private follow-up for the owner; it must not reuse an internal classifier label
such as `direct_mention`. A `request_approval` for a response must include an
exact `reply_text`; the runtime persists that draft and its `owner_action`, then
resumes the same values after approval without asking the model to rewrite them.

For a direct owner question, status update, handoff, or coordination request,
the model sends a reply only when it can provide a safe and useful response.
An assignment, investigation, or coordination reply must first complete at
least one bounded relevant read, such as reading the same-chat thread or
checking the corresponding production code. Its concise reply states what was
actually checked, the initial finding or explicit unknown, and what concrete
information was passed to the owner. Merely saying that the owner was reminded,
paraphrasing the request, or promising future work is not useful work and is
rejected before sending.

A repeated read of the same context digest, an empty or unreadable attachment,
or an unrelated source does not count as completed relevant work. Relevant Lark
images are downloaded only through `internal/lark` and the official public Go
SDK. At most two supported images are read serially, at most 1 MiB each and
2 MiB total. Raw image bytes are ephemeral model input and are never persisted.
An unavailable, oversized, unauthorized, or provider-rejected image is recorded
as explicitly unreadable, never as empty successful evidence.

High-confidence delegated investigation or coding work may send an early
progress reply only after a resumable investigation record and owner notice are
durable. The investigation then has a mandatory terminal closure: an
evidence-backed result, an owner-handled closure, or an explicit blocked
summary. Progress and final sends use separate stable action keys. Restart
resumes read-only investigation without duplicating completed progress. A
progress message may promise the required closure; no other unapproved
delegated reply may promise future delivery, coordination, or reporting.
The complete internal action key remains in the audit store, while every key
sent to Lark's public message API is a deterministic digest no longer than
Lark's 50-character UUID limit. Owner-notice and progress actions derive
different public UUIDs from their different internal action keys.

When the durable semantic gate admits a `direct_mention` or `private_message`
as still unanswered, the model must finish with a useful sender-facing `reply`
or an exact `request_approval`. It cannot override that gate with `ignore`,
`record`, or `notify`. If useful facts are unavailable without exposing private
context or inventing work, the reply states the completed bounded check and the
specific unknown or refusal instead of fabricating an answer. Withdrawal,
validated owner handling, and ordinary private continuations that need no reply
finish before the main model runs. `request_approval` holds an exact commitment
or risky response that needs the owner's approval. An automatic delegated reply
never says that the owner or team will later deliver, coordinate, or report back
unless that exact commitment has been approved. Shell output may locate evidence
but is not itself a citable source; before replying from a shell-discovered file,
the model reads that file through `read_workspace` to obtain a digest-backed
source reference.

Availability checks and simple greetings from the configured owner to the
assistant bot, including "在吗", are fast-path work. The bot replies immediately
with bot identity without requiring Lark conversation history or a model call.

Lark conversation history is optional enrichment for an already-received work
item. If that bounded history cannot be loaded, the context bundle records an
incomplete selection with a non-secret reason and continues with the current
message. A history lookup failure must not make the working reaction disappear
and leave the owner request silently waiting for retry.

The configured owner's private chat with the assistant is a trusted control
conversation. Its adjacent context keeps bounded owner messages and messages
sent by the current assistant application, including the actionable assistant
notice immediately preceding a short owner response such as "确认". App
messages remain filtered as noise in ordinary group and delegated contexts
unless they are explicitly pinned by a reply/root relation. Trusting the
current assistant's private-chat messages does not trust other applications or
allow a model instruction to widen the context policy.

User-identity Lark calls use the current Keychain credentials. If a cached
user_access_token is rejected as expired, the client serializes recovery,
reloads any newer Keychain token, and otherwise uses the current refresh_token
through the official SDK. A successful refresh rotates both tokens in Keychain
before replaying the original request exactly once. Refresh failure remains a
typed authorization error; the client never loops indefinitely or logs token
values.

All official Lark SDK clients use the agent's credential-safe logger.
Credential-bearing debug and informational SDK output is suppressed because
the WebSocket SDK includes its full connection URL in normal connection
messages. The dispatcher's fixed credential-free ready line may remain.
Warning and error output remains available only after redacting credential
query parameters and JSON credential fields. App secrets, access keys, tickets,
and tokens must never appear in daemon stdout or stderr.

Given the SDK bootstrap endpoint returns a WebSocket URL containing unique
`access_key` and `ticket` values, when the production realtime consumer
connects, disconnects, warns, or fails, then captured daemon logs contain
neither value and still retain non-secret warning/error context.

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
session creation, scheduler configuration, and startup convergence succeed,
every new process session sends one idempotent bot private message that the
agent is online before workers may claim new work. This includes manual start,
restart, upgrade, and successful recovery after an unexpected crash. Startup
automatically readmits interrupted work that is safe to recompute, leaves exact
approval/input work in an actionable waiting state with one private owner
instruction, and terminalizes externally uncertain work with one private
reconciliation summary. The notice reports the resulting resumed,
owner-action, terminalized, and uncertain counts instead of leaving an
unactionable interrupted total. Lifecycle idempotency keys must be
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

The private model environment is durable installation state. An upgrade with
an unset `OPENAI_API_KEY`, `OPENAI_BASE_URL`, or `OPENAI_MODEL` preserves that
key's installed value instead of treating absence from the installer's process
environment as a request to erase it. An explicitly supplied non-empty value
updates only that key; an explicitly supplied empty value removes only that
key. The resulting file is replaced atomically with mode `0600`, is included in
the existing rollback snapshot, and is never printed.

Given a working installation with model credentials in its private environment,
when an upgrade is launched from a shell that does not export any `OPENAI_*`
variables, then the private environment remains byte-for-byte equivalent and
the upgraded daemon remains model-configured. Given only one `OPENAI_*`
variable is explicitly supplied, when installation succeeds, then only that
key changes and all other private environment entries remain intact.

## Workspace Boundary

The configuration must contain exactly one absolute `workspace.root`. Local
rules, local file search, workspace memory references, and future local file
tools are limited to the real path of this root directory.

The boundary is enforced by path resolution, not by prompt text. Relative
paths, `..`, absolute paths outside the root, and symlinks escaping the root are
rejected before any model call or file read. Rules are loaded only from
`AGENTS.md` and `.agents/` inside the workspace.

Workspace directory, project, rule, and skill discovery is deterministic and
bounded. The first snapshot lists at most five directory levels, 600 entries,
80 entries per directory, and 16 KiB of serialized directory/project text, and
reports when entry or byte limits truncate the result. A bounded project
catalog derived from repository and manifest markers is retained before raw
leaf entries during compaction. Skills are listed by metadata and loaded in
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

Local Git history is exposed through a dedicated read-only tool rather than
shell. It accepts only a workspace-relative repository path whose resolved
repository and Git metadata stay inside the configured real workspace root.
Inherited `GIT_*` variables cannot redirect the repository, object database,
configuration, worktree, index, namespace, or replacement objects outside that
validated repository.
It returns at most 20 local commits and 8 KiB. It never fetches, checks out,
writes refs or the working tree, invokes hooks, contacts a remote, or exposes
credentials. A delegated non-owner run may use this tool because it is
read-only; this does not widen any write or external-side-effect authority.

## Coding Assistance

When a Lark message asks a software engineering question, the runtime treats it
as a coding question before the model chooses a terminal action. A coding
question is not answered by broad chat context alone. The model must either
state that the question can be answered from the current message, or create a
short investigation plan that names the code entry points, symbols, tools, and
stopping condition it intends to use.

The investigation-plan tool accepts the structured fields `question`,
`entry_points`, `symbols`, `tools`, and `stop_conditions`. When the model sends
a free-form `plan` field or otherwise malformed arguments, the rejection names
the required fields so the model can correct the call within the same bounded
run.

An insufficient-evidence coding reply normally uses a fixed conservative
template instead of model-authored prose. One narrower negative-search case is
rendered from tool receipts rather than model text: when every successfully
parsed bounded code/workspace search reports zero matches, the runtime may say
that no match was found within those bounded checks and list each query, scan
count, and truncation state. A report is parseable for this purpose only when
all four fields are explicit and well typed; null or negative scan metadata is
invalid. It must not claim global
nonexistence. Any positive match, unparseable report, or receipt set larger
than the sixteen-query display bound falls back to the conservative template
instead of inventing metadata or silently hiding evidence. A bare optional
code-index miss without a bounded workspace scan receipt also falls back.

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

The tool-result digest and each cited source digest have different meanings.
If the model copies the tool-result digest into a `source_ref`, the runtime may
canonicalize it to the recorded source digest only when the submitted relative
path and source kind identify exactly one source digest in the current run. A
source that was never observed, has a different kind, or has multiple recorded
digests for the same path and kind remains invalid; the runtime must not guess
which version the model intended.

Examples, tests, fixtures, and documentation are supporting evidence. A
definite claim about production implementation requires at least one production
source; otherwise the reply must explicitly state that production behavior
remains unverified.

Tool and context budgets must fail soft for coding investigations. Before every
model request, the runtime reports current, total, and remaining model turns
plus current, maximum, and remaining model-visible context bytes. When a run
approaches the total tool-output budget, context budget, or model-turn budget,
the runtime deterministically replaces old model/tool exchanges with one
structured checkpoint, drops obsolete raw output, and asks the model to
converge. The checkpoint preserves the original task, permissions, verified
source references, external-action receipts, explicit unknowns, and newest
messages. It must not discard the whole work item solely because a bounded
investigation produced too much raw output or restart broad investigation after
compaction. One assistant turn with multiple tool calls and all corresponding
tool-result messages is one protocol unit: compaction may bound result content
and replace oversized historical arguments with valid JSON carrying their byte
count and digest, or replace the whole older unit with a checkpoint, but it
must never retain orphaned tool results, omit their assistant tool-call message,
or insert a system progress prompt between sibling tool results. An older
parallel unit's checkpoint preserves every call ID, tool name, bounded
arguments, and matching result ID. The final provider request, including
runtime progress and terminal prompts, must stay within the configured context
limit; an irreducible protocol unit fails locally instead of being sent as an
oversized request. Repeated Lark-context reads that return no new target-message
context are treated as no-progress tool calls and force convergence.

Multimodal text and encoded image input count toward model-visible request
bytes. Ephemeral image data is sent only on the first model turn; later turns
replace it with an explicit removal marker and retain facts extracted into the
trajectory. This prevents repeated multi-megabyte payloads while making the
budget prompt reflect the actual first request.

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

Unreferenced app/bot messages are excluded from adjacent context so deployment
notifications and integration chatter cannot crowd out human discussion. An
app/bot message remains visible when it is the target, direct parent, or pinned
thread/root relation, because an explicit reply makes that message relevant.

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

When an explicit relation contains a current-app GitHub notification, context
resolution parses and verifies its canonical reference before model work. The
verified reference is persisted idempotently by Lark message ID. A restart may
load that reference, but cannot replay the notification or infer a reference
from adjacent untrusted text. Conflicting references fail closed.

When an owner sends equivalent requests through private chat and group mention
inside a short dedupe window, intake links them to one canonical work item or a
completed result. The duplicate item must not start a second long
investigation.

## Queue Recovery

Work items must not be lost when a model call, context build, or reply send
fails. A failed item is stored as `retry_wait` with the failure reason, retry
count, next attempt time, and owning online session. It may retry with bounded
backoff only while that same daemon session remains online.

At startup, every non-terminal item owned by an earlier session is first changed
to `interrupted`. Recovery stores the latest durable model step, tool call,
decision, reply action, and owner-notification action as an interruption
snapshot. After the new session is ready, a convergence pass classifies every
interruption. Work with no uncertain external action is reassigned to the new
session and safely recomputed. Exact unsent approvals remain unchanged and
produce one idempotent owner instruction. Work with an action that was executing
at interruption is never replayed; it becomes terminal with one idempotent
reconciliation summary. A missing lease timestamp permits neither a blind side
effect replay nor indefinite passive suspension.

Leases are work-kind specific and are refreshed while long runs make progress.
Fast-path and simple work have short leases. Coding questions and background
goals may have longer leases, but the lease must be compatible with their
configured loop timeout and must not leave the queue looking permanently stuck.
If the process dies and no heartbeat updates the lease, recovery marks the
latest run abandoned and snapshots the last durable stage. The next ready
session automatically readmits safe recomputable work. It does not readmit an
executing or result-uncertain side effect.
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
Approval decisions follow the same rule in the opposite direction: while the
daemon is writing, `approval approve` and `approval reject` must wait for the
bounded SQLite interval and then atomically update the exact action and work
item. They must not establish a stale read snapshot before requesting the write
lock, because a normal concurrent daemon write must not make an operator
decision fail with a snapshot-upgrade error.
An approval for work owned by an older session is assigned only when the newest
active online session is `ready`; that session owns the work before it becomes
`received`. If the newest active session is still starting, or no active
session exists, the exact action becomes `ready` but the work remains
`interrupted`. Startup convergence assigns it only after a ready session
exists. Approval never creates apparently runnable work owned by a stopped or
superseded session.

Operators use `queue inspect --work-id <id>` or
`queue inspect --message-id <id>` to
see whether a message was suppressed, queued, running, interrupted, replied, or
completed, including the exact last durable stage and any uncertain external
action. `queue resume` remains the explicit operator path for a selected
terminal, blocked, or manually paused item. Safe startup recovery does not
require it. Replaying an already terminal item requires an additional explicit
force flag.

The configured owner has the same durable control capability through commands
sent in the assistant bot's private Lark chat. `/help`, `/status`, `/doctor`,
`/tasks`, `/task`, `/approvals`, `/approval`, `/recent`, `/version`, and
`/ping` form a typed owner-private control plane. Read-only commands never call
the model. Mutation commands require exact work or action IDs and use the same
storage transitions as their CLI equivalents. An owner command in a group only
receives a private-control redirect; a non-owner command remains silent.

One canonical command catalog drives explicit parsing, `/help`, detailed help,
and the semantic-control prompt. Each catalog entry declares aliases, localized
usage, read-only or mutation risk, required typed arguments, and whether one
exact durable candidate is required. Contract tests fail when any of those
surfaces diverge.

A non-slash owner message in the assistant's private chat first passes through
a constrained semantic command resolver with the bounded current-assistant
conversation and typed eligible task/approval candidates. It returns
`not_command`, one validated catalog command, or `ambiguous`. Read-only
commands require at least `0.85` confidence. Mutations require at least `0.95`,
one exact eligible durable candidate, and normal typed command validation.
Model-produced IDs outside the supplied candidate set are rejected. Ambiguity
asks for an exact ID and performs no mutation. Ordinary questions such as
"确认一下这个修复是否上线了" remain ordinary owner requests. Explicit slash
commands remain deterministic and never use the resolver.

Every ordinary model run receives a trusted, non-secret runtime-policy
snapshot assembled from the validated active configuration. The snapshot
includes mode, assistant/delegated/private reply scopes, owner wait, semantic
owner-answer threshold, delegated direct-send threshold, retry interval, and
investigation-progress mode. It is authoritative for questions about the
assistant's current behavior. Workspace rules govern project investigation and
must never be used to infer or override these runtime facts. The model must
distinguish `policy.owner_reply_confidence_min`, which decides whether observed
conversation evidence is strong enough to classify the owner's own response,
from `policy.reply_confidence_min`, which decides whether a low-risk delegated
draft may send automatically.

`/tasks` defaults to a bounded actionable view instead of dumping historical
storage status maps. Every listed item gives a localized semantic state, its
last durable fact or failure, why owner attention is required, and an exact
currently valid next command. `/task <work-id>` includes the latest durable
stage and external-action uncertainty while excluding model chain-of-thought,
credentials, raw Lark events, open IDs, and unrestricted absolute paths.
When a delegated investigation exists, both task views also show its concrete
subject, pending/investigating/finalizing/completed/blocked state, whether the
context evidence digest is fixed, and its latest error. Active investigations
include the exact `/task <work-id>` refresh command in addition to any valid
queue recovery or cancellation command.
Lark mention placeholders in task content are never copied into reply text.
Known mentions render as `@<display-name>`; unknown placeholders render as a
localized generic person label so the durable reply layer never rejects the
control response as an unmapped mention.
Completed, ignored, cancelled, and owner-acknowledged items do not appear in the
default actionable view.
An owner resolution closes only the work-state version that existed when the
resolution was recorded. If the owner explicitly resumes that work and it
later becomes interrupted or terminal again, the newer `work_items.updated_at`
epoch no longer equals the resolution's stored `work_updated_at` snapshot, so
the old resolution becomes historical and the item returns to the default
actionable view. These values are compared for exact equality, never ordered as
variable-precision RFC3339 text.

The owner may acknowledge an audited terminal item without deleting history.
An uncertain external action must instead be reconciled as `completed`,
`not-completed`, or `unknown`. Completed closes the work without replay.
Not-completed resolves the uncertainty but requires a separate explicit resume
before work can run again. Unknown keeps the work terminal and non-replayable.
Retry, resume, and cancel reject unresolved uncertainty rather than guessing.
The exact closure forms are `/task acknowledge <work-id> <note>` and
`/task reconcile <work-id> completed|not-completed|unknown <reason>`. Local
operators use the equivalent structured commands `queue acknowledge --work-id
<id> --reason <note>` and `queue reconcile --work-id <id> --result
completed|not-completed|unknown --reason <note>`.

Owner command execution is journaled by command message identity. Parsing and
authorization happen before mutation, and execution happens only after the
ordinary work item is durably claimed. The resulting response uses the normal
durable reply-action path. If a command commits but its Lark reply fails, the
same command message returns the stored result without repeating the mutation.
The journal insert is the first SQL statement in the mutation transaction so
SQLite acquires the writer slot before duplicate-result reads and state
validation; this avoids deferred read-to-write upgrade failures during normal
daemon writes.
Bot commands and local CLI commands share typed query, transition, and
recommendation rules; CLI stdout remains structured while Lark output is
localized human text.

After an operator audits historical interrupted work, `queue cancel` can
durably close exact work/message IDs or all interrupted work except explicit
`--keep-work-id` selections. Every cancellation requires an operator reason,
preserves receipts, runs, steps, decisions, and action history, and appends a
completed `operator_cancel` audit action. It also cancels unsent approval/ready
actions and closes unresolved interruption snapshots. The command is atomic
and fails without mutation if any selected item is running, already terminal,
has an executing action, or has an unresolved interruption that observed an
executing external action. It never sends Lark messages or physically deletes
history.
Messages that were never observed because user-token polling was not configured
have no queue receipt; for those, operators use `queue backfill --chat-query
<query> --since <time> --until <time>` to create bounded intake records first.
`queue retry` has a narrower purpose: it may only accelerate ordinary
`retry_wait` work owned by the currently active session and having no executing
or blocked external action. It must never make prior-session, processing,
interrupted, terminal, or uncertain-action work claimable.

Each model run, assistant message, tool invocation, tool result, final
decision, and external action is durably recorded. On automatic or explicit
resume, old read-only evidence is audit history rather than current truth.
Interrupted read-only tools may be selected again by a new run. An interrupted
shell or IM send is marked uncertain and is never blindly repeated; recovery
first reconciles observable state. If it cannot prove whether the side effect
happened, the item becomes terminal and the owner receives one exact
reconciliation instruction instead of an indefinitely paused queue tail.

The default multi-step investigation budget is 150 model turns with a two-hour
elapsed-time ceiling. Model turns and persisted audit steps are different:
model and tool-result steps are both recorded, so one model turn can produce
multiple audit steps. Operators may configure up to 300 model turns, but the
elapsed-time, repeated-call, context, tool-output, and shell-time limits remain
independent finite boundaries. The initial system prompt states the configured
model-turn ceiling, and every model request includes its current one-based turn,
total turn budget, remaining turns, model-visible context use, context limit,
remaining context, and whether automatic compaction occurred. At 80% of either
finite budget the instruction becomes urgent and narrows work to the evidence
needed for a terminal decision.
When evidence, tool-call, repeated-call, or no-progress gates force a terminal
decision, each later request exposes only `submit_decision` and adds a direct
system instruction that previous investigation tools are unavailable. The
instruction includes the last rejection reason, reusable evidence, legal
`complete`, `partial`, or `clarification` choices, and the exact action that
must not be repeated. At most three terminal-only model attempts are allowed.
Repeated plain text or calls to unavailable tools fail the run with
`model_non_convergence` immediately after that bound instead of burning the
remaining general turn budget.

The current run maintains bounded investigation state separately from the
compacted transcript: successful receipt digests, allowed and authoritative
sources, completed tool names, exact model-submitted unknowns that reached a
typed decision, and the last terminal validator failure. The latest state is
re-injected on each terminal-only request. Raw model and tool events remain in
`agent_steps`; durable Owner summaries derive completed checks only from
successful runtime-recorded tool steps and never copy rejected model prose.

Runtime failure paths converge to one observable handling outcome:
`retry_transient`, `retry_with_changed_input`, `converge_partial`,
`ask_clarification`, `hold_for_context`, or `stop_unsafe`. A tool-result
fingerprint binds tool name, normalized arguments, success/error class, and
bounded result digest. The second identical occurrence requires a changed
strategy; with the default limit, the third identical occurrence closes broad
investigation and exposes only `submit_decision`. Unchanged deterministic
failures never start another full work-item model run.
All current evidence and grounding validators are deterministic runtime gates;
their actionable rejection is fed back into the current model run.

Only genuinely transient provider, network, and rate-limit failures use the
general retry budget. Deterministic schema, quality, evidence, grounding,
permission, no-progress, and verifier results are repaired or safely
downgraded inside the current run. If no safe sender-facing candidate can be
formed, the Owner receives a deterministic summary of successful completed
checks, the still-unresolved original result, the final blocked gate, and the
next action; rejected free-form assertions are never copied into that summary.

Provider rate limits honor `Retry-After` when present and otherwise use bounded
exponential backoff from 15 seconds to 15 minutes. A configurable retry ceiling
moves permanently failing work to dead letter instead of retrying forever.
Semantic `waiting_user` deferrals use `policy.owner_reply_max_retries`, default
3, and persist their own `owner_reply_retry_count` independently from the
general provider `retry_count` ceiling.
Schema-v16 migration moves the old `retry_count` of active v15 `waiting_user`
rows into `owner_reply_retry_count` and clears the provider counter.
`policy.owner_reply_retry` remains the interval between semantic rechecks.
Reaching the semantic ceiling atomically clears the lease and next-attempt
time, records a dead-letter reason, and records a durable owner-resolution
requirement in the same transaction. Immediate handling, periodic maintenance,
and startup recovery consume that requirement through one idempotent private
summary. The summary contains `/task <id>` and `/task resume <id> confirm`; when
a safe unsent candidate or progress exists it also states that the original
sender has not been answered and includes the candidate or completed checks,
unknowns, final hold reason, and next step. It does not require the Owner to
leave Lark for a local CLI. The target is not returned to the original
conversation and is never left in an indefinitely claimable waiting state.

A reply that passes language, identity, permission, quality, and evidence
validation is saved in schema-v16 `work_reply_candidates` before the final
owner-handled check. Candidate status is `pending`, `held`, `consumed`, or
`cancelled`. Candidate creation and every active-state transition are fenced by
the exact active work-item lease, so an expired worker cannot overwrite, hold,
consume, or cancel a newer run's draft. An `unanswered` final check sends the
exact candidate through the
normal Owner-notice-first and reply-idempotency flow, then consumes it.
`answered`, `no_reply_needed`, or `withdrawn` cancels it. `ambiguous` holds it
and defers only the semantic check. A later claim with a held candidate first
reapplies the current deterministic routing policy, then calls the semantic
resolver without calling the main model. A newly paused or blocked route
cancels the candidate. Process restart preserves the candidate. Explicit
`/task resume <id> confirm` or forced CLI resume cancels the old candidate and
starts a new investigation generation.

Runs carry model and agent-configuration fingerprints for diagnosis. A changed
runtime may safely re-evaluate non-terminal interrupted work under the current
contract, while completed, ignored, cancelled, and dead-letter history remains
terminal unless the owner explicitly uses `queue resume --force-terminal`.
Completed outward replies remain protected by the same idempotency and
reconciliation rules.

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

Only the configured owner can initiate the assistant from a group by using a
native Lark mention whose open ID or mention name matches the configured
assistant. That message becomes an `assistant_request`; it is answered with bot
identity and is not treated as a delegated owner reply. A non-owner native
assistant mention, plain-text assistant name, or private assistant message is
ignored before queueing or model work. A private assistant chat remains an
owner-only `owner_request`: the sender must be the configured owner and the
private partner must match the configured assistant. Replies to
`assistant_request` and `owner_request` messages answer the sender's own prompt,
so they do not wait for the owner, do not run the "owner already replied"
cancellation check, do not add the delegated `🤖` marker, and do not create a
delegated owner notice. Non-owner messages can trigger a sender-facing
response only by natively mentioning the configured human owner in a group
allowed by `policy.reply_scope`; that path is the read-only delegated-owner
workflow. A non-owner group message without that native Owner mention may still
be classified as Owner-relevant background information and enter bounded model
work, but its only legal terminal outcomes are `ignore`, `record`, or `notify`.
The Go runtime re-runs deterministic routing immediately before every external
reply or approval and blocks any model, resumed candidate, or legacy approval
that attempts to promote such a message to `reply` or `request_approval`.

The router attaches a work kind and priority to every accepted item. Assistant
and owner requests that match a fast-path command are `fast_path` work. Requests
that need a short answer but no code evidence are `simple_question` work.
Engineering requests that require code evidence are `coding_question` work
unless they explicitly need durable follow-up, in which case they become
`coding_goal` work with persisted completion and blocking conditions. Explicit
requests for source code, production or code entry points, APIs, handlers, or
database evidence are code-evidence requests. Merely mentioning the configured
Workspace or a business warehouse is not enough. Routing and runtime evidence
validation use the same classifier.

For `coding_question` work, a successful authoritative `read_workspace` result
containing a production source starts convergence. Code-index and
workspace-search results only locate candidates and cannot trigger convergence
before the production file is actually read. If that production read answers
every requested fact, the model must stop broad investigation and submit its
decision. If it proves only part of a multi-field request or only the container
type of a serialized value, the remaining bounded search and read tools stay
available for the unanswered facts. The model must answer from verified facts
and state any remaining unknowns explicitly.
A definite coding reply must declare `evidence_status=verified` and cite at
least one production source returned by an authoritative `read_workspace`
result in the current run. A reply that cannot make a definite claim declares
`evidence_status=insufficient`; the runtime replaces its free-form reply text
with a canonical evidence-limited response so an unknown marker cannot be mixed
with an unsupported definite inference. An insufficient coding reply is only
accepted after at least one successful workspace/code search, trace, explore,
or read in the current run; reading Lark history alone does not count as code
investigation. Search-only candidate sources cannot support a definite code
claim.

`reply_outcome=partial` does not weaken this rule. Every definite code statement
inside a partial reply must still be grounded in the cited authoritative reads.
The unknown portion must remain explicit and cannot be mixed with an
unsupported inference. `reply_outcome=clarification` is accepted without code
investigation only when the current input lacks the exact path, content,
attachment, or referent needed to perform a meaningful bounded read.

When the requested fact is the concrete shape of an opaque serialized value,
reading only a language-level declaration such as `String`, `[]byte`, or
`json.RawMessage` is incomplete evidence. The investigation must use its
remaining bounded calls to read a current documentation example, test fixture,
protocol definition, or serialization implementation that exposes structural
serialization evidence such as a concrete object example or serializer
operation. A verified answer to that request is rejected when its cited
current-run reads contain only opaque declarations.
Concrete-shape intent is evaluated within one business-message semantic unit.
The current request can use one explicitly linked or clearly continued prior
message to resolve a referent, but unrelated conversation entries are never
concatenated into one keyword bag. A shape word in one historical message and
`String`, JSON, or another serialization word in a different message cannot
turn a current field-declaration or symbol-existence question into a
serialized-shape request.
A request to format, lay out, or reorder the assistant's response is not a
serialized-shape question merely because it contains the word `format`. Such a
presentation instruction never borrows a serialized target from conversation
history. A context-dependent shape follow-up must itself ask to inspect,
explain, show, supplement, or otherwise determine the missing structure or
format. English container terms such as `String`, `body`, and `payload` are
matched as complete lexical tokens; identifiers such as `StringUtils` do not
become serialized targets merely because they contain one of those terms.
When the question names a concrete code field such as `sampleContent`, the
structural example or serializer operation must occur in the local evidence
context of that field. An unrelated JSON object or serializer elsewhere in the
same or another cited file does not complete the field's evidence.
The concrete JSON example stated in the reply must also occur in that
field-related local evidence. A matching JSON object from an unrelated cited
source cannot support a different claimed shape.
When one reply answers several related protocol facts, JSON examples that do
not claim to be the named field's shape are not compared against that field's
local structural snippet. Every concrete JSON example in the reply must still
occur verbatim after whitespace normalization in at least one cited
current-run read, while JSON examples locally bound to the named field must
additionally occur in that field's local evidence. Thus a cited response body,
push payload, or converged local-state example may accompany a `sampleContent`
answer without being mistaken for the `sampleContent` shape, but an invented JSON
object or an unrelated object presented as `sampleContent` is rejected.
Reply-side field binding is evaluated per JSON example rather than by reusing
source-snippet extraction over a whole line. A standard `unknown/next step`
suffix after an example does not erase the binding, and a later response,
push, notification, or local-state fact marker ends the earlier field binding
even when several JSON examples share one line.
If a verified coding draft reaches final grounding but fails only because its
reply text contains an uncited path, identifier, callback, field, or serialized
example, the runtime preserves one bounded submit-only correction turn outside
the investigation budget. The model must narrow or rephrase the answer against
the authoritative current-run reads retained by the runtime. It must not
reinterpret a reply-local grounding rejection or context compaction as loss of
the underlying evidence and downgrade to `insufficient` for that false reason.
A newly identified, precisely stated evidence gap may still converge to the
normal fixed insufficient reply instead of being forced into a verified claim.
The correction turn exposes no investigation or external-action tools, does
not expand Workspace scope, and is granted at most once.
As soon as a current-run read proves only the opaque declaration and at least
two model turns remain before the terminal turn, the runtime enters bounded
structural-evidence recovery. It exposes only `search_workspace` for one exact
field-name search across the whole already selected repository scope. The
model may omit `path` or repeat that exact scope, but cannot narrow the search
to a child directory and cannot supply `max_results` to truncate recovery.
This one dedicated recovery search remains available even when earlier generic
workspace searches have reached the source-less-search limit. Because the
runtime itself selects this exact field-name recovery, it also remains
executable when the model reached the opaque declaration through a direct read
before submitting an investigation plan; the plan-first gate continues to
apply to model-chosen broad searches. Only search
results whose local snippet contains both that field and concrete structural
evidence become readable recovery candidates. The following turn exposes only
`read_workspace`, and execution accepts only one of those candidates. This
recovery search and read happen before the model may submit an
`evidence_status=insufficient` decision, so a voluntarily early terminal
decision cannot discard evidence that the bounded recovery can locate.
If this structural gap remains at the start of the penultimate model turn and
an investigation call is still available, the runtime must reserve that turn
for exactly one `read_workspace` call rather than switching early to
terminal-only mode. The evidence-completion prompt identifies the missing
structural fact and requires a known path; it does not reopen candidate
discovery. After that read, or when no investigation call remains, the final
turn is terminal-only and must submit verified facts with explicit unknowns.

A verified reply may mention repository-relative file or directory paths only
when those paths identify cited sources returned by `read_workspace` in the
current run. Search-only candidates are not readable evidence even when the
model copies their source references into the final decision. This applies to
paths with extensions, directories, and conventional extensionless repository
files such as `Makefile` and `Dockerfile`. Lower-camel-case code identifiers in
the reply must occur as complete identifiers in the cited authoritative reads;
this prevents a nearby but different field or callback name from being
presented as verified.

## Durable Memory And Feedback

Agent memory is durable SQLite state, not a process-local slice. Each entry has
a stable ID, kind (`fact`, `preference`, `project`, or `response_feedback`),
scope (`global` or a workspace-relative project key), bounded content, optional
source work/message identity, confidence, timestamps, and an optional deletion
tombstone. Feedback records an owner verdict and bounded note against an entry.

Raw chat transcripts, credentials, model chain-of-thought, host descriptions,
and unverified model guesses are never stored as memory. Owner-authored
corrections and explicit owner-private `/memory add` commands may create
prompt-visible entries. Automatic extraction creates only a candidate until
the owner confirms it. Automatic extraction accepts at most one bounded stable
fact, preference, project fact, or response evaluation from the current
owner-authored assistant-private message. It discards credential-like content
and exact duplicate candidates; it never copies the surrounding transcript or
assistant-authored text.

Memory retrieval is bounded by scope, query terms, confidence, recency, count,
and serialized bytes. Deleted and unconfirmed entries are excluded from model
context. `/memory list`, `/memory add`, `/memory delete`, and
`/memory feedback` use the same typed owner-private control catalog and journal
as other commands. Restart preserves accepted entries and feedback.

- Given an ordinary owner business question contains no stable correction or
  preference, when semantic command classification runs, then it creates no
  memory.
- Given the owner states one stable correction or preference without a slash
  command, when semantic classification returns a bounded memory candidate,
  then the candidate is persisted once with the current message as its source
  but remains absent from later model context until the owner confirms it.
- Given memory content contains a common provider token, cloud access key,
  private key, or password/secret assignment, when any bot or local memory
  command validates it, then no plaintext memory row is written.

## Reply Policy

The owner profile contains a concrete display name and language policy.
Configured language takes priority; `auto` infers Chinese or English from the
bounded current conversation and then uses the configured fallback. All
outward explanatory prose in one message uses that resolved language. Internal
model reasoning is never pasted into owner notices. Sender-facing replies,
their owner notices, and terminal summaries for the same work item use the
same resolved language. Lifecycle messages with no bounded conversation use
the configured fallback.

For delegated direct mentions and human private messages, the sent body is
deterministically wrapped as an intelligent-assistant response and states that
the concrete owner name was notified. It never calls the owner a generic
"user". Owner-originated assistant requests do not receive this delegated
wrapper. The owner notice is durably completed before this wrapped reply is
sent; a known notification failure blocks the sender-facing reply, and an
interrupted notification with an uncertain result is never replayed.

Before replying as the owner, the runtime must:

1. build a source-backed draft;
2. wait for the configured owner-response window;
3. check identity, blocked-target, and configured-scope gates;
4. re-read or re-check the thread state;
5. cancel if the owner already replied, the message was withdrawn, or the
   question was solved, before considering confidence or approval;
6. check risk, confidence, and approval policy;
7. notify the owner through the bot with the exact intended reply and whether
   any owner action remains;
8. immediately recheck terminal thread state and send with a stable
   idempotency key only when it is still unanswered.

The default installed delegated reply confidence floor is `0.70`. A verified
low-risk reply at or above that floor sends automatically in auto mode.
Medium/high risk, personal commitments, destructive operations, insufficient
evidence, or approval mode still hold or block the exact action. Pending
approvals repeat the owner-handled/withdrawn/solved recheck before execution;
a stale approval is cancelled and audited instead of sending an old draft.

Reply decisions must include non-empty model-authored text. There is no generic
acknowledgement fallback. `notify` performs a real owner notification,
`request_approval` enters a durable actionable state, `record` persists an
auditable trajectory, and ignore/reply outcomes preserve the actual action
status rather than treating blocked or awaiting actions as completed.

Delegated group replies have an explicit `policy.reply_scope`:

- `all_groups` allows a direct owner mention from every user-visible group to
  pass the reply-scope gate;
- `configured_groups` allows delegated replies only in groups discovered by
  the daemon's `--chat-query`, and startup fails if that query is empty.

`all_groups` is the default. `--chat-query` still discovers and marks the
primary validation group, but it does not restrict delegated replies while
`reply_scope` is `all_groups`. Reply scope does not bypass blocked chats or
users, model relevance and risk checks, confidence and approval policy, the
owner wait window, withdrawn-message and owner-already-replied checks, or
idempotent sending. Changing reply scope never replays completed, ignored, or
terminal historical work. Safe non-terminal interrupted work follows startup
convergence independently of a scope change.

The configured `policy.reply_confidence_min` applies uniformly. Direct owner
mentions and assistant-facing requests do not receive a hidden lower confidence
floor; a draft below the configured threshold waits for approval.
Every `reply` decision must explicitly provide `reply_confidence`. Omitting the
field is an invalid model response that remains inside the bounded model repair
loop; omission is never interpreted as confidence zero and must not silently
turn an otherwise valid assistant reply into an approval.

Group requests addressed to the assistant have an independent
`assistant.reply_scope` with the same values:

- `all_groups` allows the configured owner's native assistant mention from
  every bot-visible group;
- `configured_groups` allows the configured owner's assistant mentions only in
  groups discovered by the daemon's `--chat-query`, and startup fails if that
  query is empty.

`assistant.reply_scope` is also `all_groups` by default. In
`configured_groups` mode, startup resolves the query to concrete group IDs
with bot identity before intake starts, following every result page. Those bot
resolved IDs are projected into a dedicated assistant-scope marker for both
real-time and polling events; the user-identity configured-group marker remains
independent for delegated owner replies. A query that resolves no group is an
explicit startup error. Both scope checks run before model work and again before
sending. Neither scope bypasses the global blocked chat/user lists, model risk
and confidence checks, approval policy, message withdrawal checks, or
idempotent sending.

`--dry-run` uses the same intake, context, and model decision path but does not
execute the reply tool. Initial live validation should run dry-run across
visible conversations, then allow one bounded authorized chat reply. Live
validation targets are operational constraints and are independent from the
configured reply scope.

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

The GitHub-Lark bridge additionally requires executable integration coverage
for HTTP-only notification sending, stable Lark idempotency keys, hostile
pull-request text, verified and spoofed quoted references, owner and non-owner
permission behavior, bounded GitHub results, truthful API failures, and
reference recovery after restart.

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
- Given `assistant.reply_scope` is `all_groups` and the configured owner
  natively mentions the assistant in a group that does not match
  `--chat-query`, when routing runs, then the request enters model or fast-path
  work and a reply uses bot identity without an owner-delegation marker or
  delegated owner notice.
- Given a non-owner natively mentions the assistant in any group, regardless of
  `assistant.reply_scope`, when real-time or polling intake evaluates it, then
  the request is ignored before queueing, model work, working reactions, or any
  reply.
- Given a non-owner group message contains relevance words such as `任务` and
  may mention other people, but does not natively mention the configured owner,
  when deterministic routing classifies it as inferred background work and the
  model, a resumed candidate, or a legacy approval attempts `reply` or
  `request_approval`, then the final Go send gate re-runs current routing,
  preserves only a non-sender-facing `ignore`, `record`, or `notify` outcome,
  and creates no group reply action.
- Given `assistant.reply_scope` is `configured_groups` and the configured owner
  mentions the assistant outside the groups discovered by `--chat-query`, when
  real-time or polling intake evaluates it, then the request is ignored before
  model work and cannot pass the final send gate.
- Given bot and user identities resolve different groups for the same
  `--chat-query`, when polling observes an assistant mention, then assistant
  scope uses only the bot-resolved group IDs and does not reuse the delegated
  owner scope marker.
- Given bot chat search returns multiple pages, when configured assistant scope
  starts, then every page is consumed with bot identity and every matched group
  is allowed consistently by real-time and polling intake.
- Given `assistant.reply_scope` is `configured_groups` but the daemon has no
  `--chat-query`, or the query resolves no group, when live options are built,
  then startup fails explicitly instead of silently ignoring all assistant
  requests.
- Given `assistant.reply_scope` contains an unsupported value, when
  configuration is loaded, then validation fails and names the
  `assistant.reply_scope` field.
- Given a non-owner privately messages the assistant, natively mentions the
  assistant in a group, or writes the assistant name in group text without a
  native mention, when intake or routing runs, then the runtime remains silent
  and ignores it before queueing or any model call.
- Given the configured owner sends an ordinary message or question to another
  human in a private chat, when user-token polling observes it, then the
  message is discarded before durable work intake and cannot fall through to
  inferred relevance, model work, or a reply to the owner's own message.
- Given another human answers a question initiated by the owner in private
  chat and does not add a new question or request, while the owner continues
  the same conversation, when semantic delegated-reply resolution runs, then
  it returns high-confidence `no_reply_needed` and neither the main reply model
  nor a sender-facing reply runs.
- Given the owner privately asks why a remembered group-member-count limit
  cannot be found and another human replies that it was only discussed without
  detailed interaction or design, when semantic delegated-reply resolution
  runs, then the target is treated as an answer to the owner's question rather
  than a new coding task, returns `no_reply_needed`, and does not start
  investigation.
- Given the same private answer adds a new explicit request such as `你帮我查一下代码`,
  when semantic delegated-reply resolution runs, then that exact request quote
  may satisfy the unanswered obligation check and enter the delegated workflow.
- Given the configured owner adds `Get`, `OK`, `DONE`, `THUMBSUP`,
  `CheckMark`, `Yes`, or `LGTM` to a delegated target, when semantic resolution
  or the final owner-handled check runs, then the target is treated as already
  handled; unsupported, bot-authored, or other-user reactions do not suppress
  the workflow.
- Given an ordinary private message contains a new question, request,
  invitation, or coordination need that the owner has not handled, when the
  semantic deadline is evaluated, then it remains `unanswered` and may enter
  the read-only delegated workflow.
- Given a group message explicitly mentions the owner with only a reaction,
  compliment, or social acknowledgement and no new action obligation, when
  semantic resolution runs, then it becomes `no_reply_needed` and does not
  enter retry/dead-letter owner-summary flow.
- Given a group message explicitly mentions the owner with a clear request such
  as asking the owner to confirm, investigate, look into, or handle something,
  when semantic resolution runs, then a model-provided `no_reply_needed` result
  is rejected and the target remains delegated owner work unless owner content
  handled it.
- Given the owner participated before a newer group message explicitly
  mentions the owner with substantive declarative feedback, when semantic
  resolution runs, then the older owner message does not count as handling the
  newer target and the target remains unanswered.
- Given the messages before a delegated target discuss a production
  sample-event failure, a nearby image contains `1408 SampleEventDisabled`, the
  target says the sender's computer disconnected, and later messages clarify
  that production is not deployed, when the semantic gate and main Agent run,
  then their shared context identifies message editing as the task, preserves
  the later clarification through the semantic cutoff, and never treats the
  sender's network as the investigation subject.
- Given a contextual target has no coding keyword but its shared task summary
  is a high-confidence production-code investigation, when the runtime budgets
  the run, then coding read tools and coding turns are available without
  widening non-owner write authority.
- Given a relevant Lark image is within the bounded image limits, when the
  configured model supports image input, then the Agent may use its contents
  as ephemeral evidence; when it is unavailable, over limit, unauthorized, or
  rejected by the provider, then the Agent states the exact evidence gap and
  does not invent image contents.
- Given high-confidence contextual work requires durable investigation, when
  the semantic gate admits it, then one owner notice and one progress reply are
  recorded before Agent work and one final, owner-handled, or blocked closure
  eventually completes the investigation.
- Given an investigation action's full internal idempotency key is longer than
  Lark's public message UUID limit, when the owner notice and progress reply are
  sent, then the audit store retains the full distinct keys while Lark receives
  stable, distinct UUID digests of at most 50 characters.
- Given the daemon restarts after the progress reply completed, when recovery
  runs, then progress is not duplicated, the original normalized context
  snapshot is restored without image bytes, initial classification is not
  repeated against a new cutoff, and read-only investigation resumes.
- Given an ambiguous target replies to a root or thread message outside the
  adjacent chat page, when semantic context is selected, then the readable
  relation is included; if it cannot be read, resolution is incomplete and no
  antecedent is invented.
- Given a delegated group mention or human private message passed the semantic
  gate as unanswered, when the main model submits `ignore`, `record`, or
  `notify`, then the runtime rejects that terminal decision and requires a
  useful sender-facing `reply` or an exact `request_approval`.
- Given `policy.reply_scope` is `all_groups` and another sender directly
  mentions the owner in a group that does not match `--chat-query`, when the
  reply passes all other policy checks, then the agent may reply as the owner
  and the query is not used as a final reply gate.
- Given `policy.reply_scope` is `configured_groups` and another sender directly
  mentions the owner outside the groups discovered by `--chat-query`, when the
  final reply gate runs, then the reply is blocked as outside the configured
  scope.
- Given `policy.reply_scope` is `configured_groups` but the daemon has no
  `--chat-query`, when live options are built, then startup fails with a
  configuration error instead of silently blocking every delegated reply.
- Given `policy.reply_scope` contains an unsupported value, when configuration
  is loaded, then validation fails and names the `policy.reply_scope` field.
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
- Given an assistant request asks to inspect source code, a production/code
  entry point, an implementation file, a test file, or a related test inside
  the configured Workspace and report evidence,
  when deterministic routing classifies the work, then it enters
  `coding_question` rather than `simple_question`, so code investigation tools
  and the evidence-backed conclusion flow are available.
- Given a foreground coding investigation is active, when owner sends a new
  fast-path request, then the scheduler processes the fast-path item before the
  background or lower-priority coding work.
- Given two foreground workers are ready to persist agent runs while another
  process briefly holds the SQLite write lock, when that lock is released within
  the configured wait interval, then both run starts persist without a
  `database is locked` failure.
- Given the daemon holds a brief SQLite write transaction while an exact action
  is awaiting approval, when an operator approves or rejects that action and the
  daemon releases the lock within the configured wait interval, then the
  operator command completes the exact decision atomically without a stale
  snapshot failure.
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
- Given one coding model turn emits both a useful bounded code lookup and a
  policy-rejected shell search, when no-progress accounting is updated, then
  that whole turn records progress instead of consuming one no-progress slot
  per sibling tool call.
- Given a coding model sends malformed free-form investigation-plan arguments
  after earlier policy rejections, when the plan tool rejects them, then the
  rejection names all required structured fields and a corrected plan plus
  bounded workspace search can still finish within the original run.
- Given a false-premise coding question receives only successfully parsed
  zero-match bounded search reports, when an insufficient-evidence reply is
  normalized, then the runtime says no match was found in those checks and
  lists the actual queries, scan counts, and truncation state without claiming
  that the symbol globally cannot exist.
- Given any successful code search reports a candidate match or a search report
  cannot be parsed, when an insufficient-evidence reply is normalized, then
  the runtime discards the model prose and keeps the fixed conservative
  evidence-limited template.
- Given up to sixteen zero-match search receipts are collected, when an
  insufficient-evidence reply is normalized, then the runtime lists the full
  deduplicated receipt set rather than discarding useful bounded evidence.
- Given more than sixteen zero-match search receipts are collected, including
  repeated queries, when
  an insufficient-evidence reply is normalized, then the runtime keeps the
  fixed conservative template instead of hiding undisplayed receipts.
- Given the optional code index returns an empty result without bounded scan
  metadata, when an insufficient-evidence reply is normalized, then the runtime
  keeps the fixed conservative template rather than inventing scan coverage.
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
- Given a workspace read records one source digest and the same receipt exposes
  a different tool-result digest, when the model cites the correct relative path
  and source kind but copies the tool-result digest, then the runtime
  canonicalizes the citation to the unique recorded source digest and validates
  the decision; when two recorded digests share that path and kind, the same
  mismatch is rejected as ambiguous.
- Given the coding run approaches tool-output, context, or turn budget, when
  useful evidence already exists, then the runtime summarizes stale evidence and
  forces convergence instead of failing the whole work item only because raw
  output exceeded a bound.
- Given one model turn requests multiple read-only tools and their combined
  results trigger context compaction, when the next model request is built,
  then the assistant tool-call message and every matching tool-result message
  remain in one uninterrupted protocol unit, with any convergence prompt placed
  after the final sibling result; the provider receives no orphaned or missing
  tool-call identifiers.
- Given an older parallel tool unit is replaced by a structured checkpoint,
  when the checkpoint is built, then each sibling call and result keeps its
  call ID, tool name, and bounded arguments so same-tool calls remain
  distinguishable.
- Given the latest complete tool unit has oversized arguments or many bounded
  results, when compaction builds the next provider request, then historical
  arguments become valid digest-bearing JSON and result text is reduced until
  the whole request including runtime prompts fits the configured context
  limit; if protocol identifiers alone cannot fit, the runtime fails locally
  and does not send an oversized request.
- Given a coding question names an exact function and a digest-backed read
  establishes every concrete field requested by the user, when the next model
  turn starts, then the model is told to submit the decision without expanding
  into unrelated chat or call-site searches unless reachability was requested.
- Given a coding question asks for several code facts and the first
  digest-backed production read supports only some of them, when the next model
  turn starts, then the remaining bounded search and read tools stay available
  so the model can satisfy the unanswered plan stop conditions.
- Given the user names an exact repository or workspace-relative path, when a
  coding investigation selects entry points, then it preserves the named
  spelling and case and does not substitute a similarly named sibling project.
- Given the user names a path prefixed by the configured workspace directory
  name, when a coding tool receives that same prefixed path, then the runtime
  canonicalizes it to the equivalent workspace-relative path before enforcing
  the exact subtree boundary.
- Given an exact repository scope is active and `search_workspace` omits its
  optional path, when the runtime prepares the tool call, then it injects the
  exact scope and the bounded search scans only that subtree; sibling projects
  cannot contribute candidates or source references.
- Given a bounded workspace search contains several whitespace-separated code
  terms, when the exact phrase is absent but every term occurs in one file,
  then that file is returned as a candidate; files missing any term do not
  match.
- Given an exact repository scope is workspace-relative, when it is resolved,
  then its top-level component must match the bounded workspace directory
  snapshot and it must be the only matching workspace-relative path in that
  message; an API route or a comparison naming multiple repositories is not
  inferred as one hard workspace scope.
- Given the current message explicitly continues a previous message, when an
  exact repository scope is inherited, then it comes from the most recent
  bounded same-sender message; unrelated history, another sender, or a bot
  message cannot impose a hard workspace scope.
- Given an exact repository scope is inherited from bounded same-chat context,
  when a coding run starts, then the model is explicitly told that the scope is
  already a readable subtree inside the configured workspace root, every path
  must stay inside that subtree, and changing the workspace root is neither
  necessary nor an acceptable substitute for investigation.
- Given an exact repository scope is active, when the model selects coding
  tools, then only path-scoped workspace discovery and read tools remain
  visible together with same-chat context and terminal control tools; global
  symbol, call-path, exploratory, shell, workspace-rule, and skill readers are
  omitted because they cannot prove the exact subtree boundary.
- Given an exact repository scope is active and a path-scoped tool supplies a
  path relative to that repository, when the runtime prepares the call, then it
  prefixes the exact scope before execution. A differently cased similarly
  named repository remains an explicit sibling substitution error and is never
  rewritten into the requested project.
- Given the Owner names an exact repository in one same-chat message and the
  next message from that Owner asks to answer from "the current project",
  "this project", or the equivalent Chinese wording, when the runtime resolves
  the coding scope, then it treats that wording as a reference to the most
  recent same-sender repository path and activates the exact-scope boundary.
  A same-chat path from another sender is never inherited.
- Given a path inside the exact repository scope is a symbolic link to a
  sibling project elsewhere in the configured workspace, when list, search, or
  read prepares to execute, then the runtime resolves the real scope and target
  paths and rejects the call before any sibling content reaches the model.
- Given a path relies on case-insensitive filesystem lookup, when any existing
  path component differs from its on-disk spelling, then the exact-scope gate
  rejects the call instead of allowing a differently cased repository or file.
- Given a coding question asks for several related facts in one real project,
  when the exact scope is active, then the run is instructed to use at most two
  bounded locating searches before reading the candidate production files and
  to reserve enough tool and model budget to answer every requested field or
  state the exact remaining unknown.
- Given a coding question asks for the concrete structure of a serialized
  string field, when the first production declaration proves only that the
  field is a string, then a verified decision is rejected until a bounded
  documentation, fixture, protocol, or serialization read supplies structural
  serialization evidence.
- Given prior conversation messages separately mention JSON shape and a
  `String` field, but the current coding question asks only which fields a
  production class declares and whether a named symbol exists, when structural
  intent and final grounding are evaluated, then keywords from those unrelated
  messages are not combined and a correctly cited field answer is accepted.
- Given the preceding message mentions a serialized `String` target but the
  current coding request only instructs the assistant to use a response format
  or layout, when structural intent is evaluated, then that presentation
  instruction does not borrow the preceding target and a cited field answer
  does not require unrelated serialized-shape evidence.
- Given a context-dependent shape follow-up uses an imperative such as
  "supplement the concrete structure", when its explicitly linked or nearest
  valid message names a serialized target, then structural evidence remains
  required; an unrelated identifier such as `StringUtils` does not resolve that
  target.
- Given that opaque declaration is read while at least two model turns remain
  before the terminal turn and no field-related structural source has been
  read, when the next model request is prepared, then only
  `search_workspace` is exposed and exactly one search for the named field is
  allowed across the existing exact repository scope; a differently cased
  query, model-supplied child `path`, or model-supplied result limit is rejected
  before execution.
- Given earlier generic workspace searches have reached the source-less-search
  limit but intervening reads still locate an opaque declaration of the named
  field, when structural recovery begins, then its one dedicated exact-field
  search executes instead of being rejected by the generic search limit.
- Given the model directly reads an opaque declaration before submitting an
  investigation plan, when the runtime enters structural recovery, then the
  runtime-selected exact-field search executes instead of being rejected by the
  plan-first gate; unrelated model-chosen broad searches still require a plan.
- Given that structural recovery search returns several candidates, when their
  local snippets are evaluated, then only paths whose snippet contains both
  the named field and a concrete example or serializer become candidates; the
  next request exposes only `read_workspace` and rejects reads outside that
  candidate set.
- Given a candidate snippet says that the named field's structure is unknown or
  unavailable and a nearby line contains a different field's JSON example,
  when candidate filtering and final grounding run, then that nearby JSON is
  not bound to the named field and the path is not accepted as structural
  evidence.
- Given one verified reply includes the cited `sampleContent` example plus cited
  response-body, push-payload, or local-state JSON examples, when final
  grounding runs, then every JSON example is checked against the union of
  cited current-run reads and only the JSON locally presented as `sampleContent`
  is additionally checked against `sampleContent`-local evidence; the other cited
  protocol JSON examples do not cause a false rejection.
- Given any JSON example in that multi-fact reply does not occur in any cited
  current-run read, when final grounding runs, then the verified reply is
  rejected even if the `sampleContent` example itself is correct.
- Given a reply places the named-field JSON and a cited response JSON on the
  same line, or places the standard `unknown/next step` suffix after the named
  field JSON, when reply-side field binding runs, then each JSON keeps its own
  fact association: the response JSON is not compared as the field shape and
  the suffix does not allow unrelated evidence to masquerade as that shape.
- Given a verified coding draft cites authoritative reads but introduces an
  unsupported reply-local identifier such as `eventCode` for a source that
  only says notification `9001`, when final grounding rejects that wording,
  then the runtime grants one submit-only correction turn, rejects an immediate
  evidence-loss downgrade, and accepts a corrected verified answer that says
  notification `9001` while preserving the cited callback and message fields.
- Given the model tries to submit `evidence_status=insufficient` before the
  bounded structural recovery is complete, when the terminal gate evaluates
  the draft, then it rejects the early conclusion and directs the model through
  the remaining recovery search and read rather than creating an approval
  draft.
- Given that opaque production declaration is the only structural evidence at
  the start of the penultimate model turn and one investigation call remains,
  when the runtime prepares the model request, then only `read_workspace` is
  exposed and the model is told to spend exactly that turn reading one known
  current documentation, fixture, protocol, or serializer path; broad locating
  tools and `submit_decision` are unavailable until the final turn.
- Given the current reads contain an unrelated JSON object or serializer but
  no structural evidence in the local context of the field named by the
  question, when convergence and final verification evaluate the evidence,
  then the unrelated structure does not suppress the reserved read and cannot
  support a verified answer for that field.
- Given one cited source contains a valid field-related example while another
  cited source contains a different unrelated JSON object, when the verified
  reply claims the unrelated object as that field's concrete shape, then final
  verification rejects the claimed example even though its bytes occur
  somewhere in the cited source set.
- Given the targeted evidence-completion read has run, or structural
  serialization evidence was already available, when the final model turn
  starts, then only `submit_decision` is exposed and the answer must cite the
  current-run reads or state the exact remaining unknown without invention.
- Given a verified coding draft names a repository-relative path that was not
  cited from a current-run read, including a search-only path, directory, or
  extensionless repository file, or names a lower-camel-case code identifier
  absent from all cited authoritative reads, when the verify gate evaluates the
  draft, then it rejects the draft and gives the model a chance to repair the
  evidence or wording before any Lark reply is sent.
- Given a coding run submits its required bounded investigation plan, when the
  runtime reports the current tool-call budget, then the plan is control
  metadata and does not consume an investigation call; every model turn sees
  the current and maximum investigation-call counts together with the remaining
  model-turn and context budgets.
- Given an exact repository scope is active, when the model calls a workspace
  tool that cannot carry that path boundary, including global symbol search or
  call-path tracing, then the runtime rejects it and requires a path-scoped
  search or read.
- Given a coding run has citable workspace evidence and enters its final two
  model turns, when the model attempts another investigation tool, then the
  runtime rejects that tool call and preserves the final turn for
  `submit_decision` instead of failing and retrying the entire run, except for
  the single structural-evidence completion read defined above.
- Given repeated `get_lark_context` calls return no new target-message context,
  when the model asks again, then the runtime rejects the no-progress call and
  requires a decision or a different evidence tool.
- Given a coding reply is ready to send, when the verify gate checks it, then
  the reply is sent only if it addresses the original question, is supported by
  cited code evidence, and obeys current policy; otherwise the draft is repaired
  or held for approval.
- Given an assistant group request or private owner request is held for
  approval, when the exact draft is approved and resumed without another model
  call, then the persisted assistant/owner-request relevance selects bot
  identity, no delegated robot prefix is added, and no delegated owner notice
  is created.
- Given a delegated owner mention is held for approval, when its exact draft is
  approved and resumed, then the persisted direct-mention relevance selects
  user identity, completes the delegated owner notice, and only then sends the
  sender-facing reply with the delegated robot prefix.
- Given a delegated reply is held because its confidence is below policy or the
  model explicitly requests approval, when the durable approval is created,
  then the owner receives a private notice saying the draft has not been sent
  and showing the approval ID, exact draft, remaining owner action,
  `/approval approve <id> confirm`, and `/approval reject <id> <reason>`;
  it does not first send a separate generic preparation notice.
- Given an unanswered delegated reply meets or exceeds the configured confidence
  threshold and has no approval-only commitment or policy risk, when the final
  reply gate passes, then the agent privately notifies the owner and immediately
  sends the sender-facing reply without waiting for owner confirmation.
- Given delivery of that approval notice is retried, when the same durable
  approval action is observed again, then the stable notification idempotency
  key prevents a duplicate private message.
- Given an ordinary auto-mode reply has no current or legacy approved draft,
  when the reply controller checks for a reusable approval, then the storage
  layer returns "not found" without requiring a persisted legacy relevance and
  the normal reply continues. Legacy relevance is read and validated only when
  a matching ready legacy approval action actually exists.
- Given an exact reply approval was written before action requests stored
  relevance, when the approved work resumes after upgrade, then relevance is
  restored from the work item's durable decision, the legacy exact-draft key is
  atomically consumed, and the legacy approval action becomes completed with
  the returned Lark message ID.
- Given neither the approval request nor the durable work decision contains a
  recognized relevance, when approval recovery runs, then it fails explicitly
  before selecting bot or user identity.
- Given the default agent configuration, when a deep investigation starts,
  then it has 150 model turns and a two-hour ceiling; given an operator selects
  a custom budget, then values through 300 are accepted and larger values fail
  validation.
- Given a configured model-turn budget, when the initial and subsequent model
  requests are built, then the system instructions derive the total, current,
  and remaining turns from that runtime value rather than a duplicated literal.
- Given a human message directly mentions the owner with a status update,
  coordination request, commitment, or follow-up, when the model triages it,
  then it treats the message as addressed to the owner workflow and, after the
  semantic gate finds it unanswered, chooses a useful reply or exact approval
  instead of dismissing or silently recording it.
- Given the configured owner privately messages the assistant chat, when the
  message is polled, then it enters the model as an owner-request work item and
  the assistant replies with bot identity without a redundant owner notice.
- Given the configured owner privately asks the assistant "在吗", when the item
  is processed, then the bot replies "在的。" as fast-path work and removes the
  working reaction without loading conversation history or calling a model.
- Given the configured owner mentions the assistant in an allowed group and
  asks "在吗", when the item is processed, then the bot replies "在的。" through
  the same fast path without loading conversation history or calling a model.
- Given a non-fast-path owner request has already been received and bounded Lark
  history loading fails, when context is built, then the current message still
  reaches the model and the bundle marks its context selection incomplete.
- Given `policy.owner_reply_confidence_min` is `0.85` and
  `policy.reply_confidence_min` is `0.70`, when the owner asks an ordinary
  question containing "确认" about the assistant's current thresholds, then
  semantic control returns `not_command`, the model receives both trusted
  values with their distinct meanings, and it does not infer current runtime
  policy from workspace project rules.
- Given a user-identity Lark request rejects the cached access token as expired
  and Keychain contains a newer token, when the request recovers, then it reloads
  and replays once without consuming the refresh token.
- Given no newer access token exists but a refresh token is available, when the
  request recovers, then the official SDK rotates both tokens, persists them,
  and replays the original request once.
- Given the configured owner mentions the assistant bot in an allowed group,
  when the message is observed by real-time intake or polling, then it enters
  as an assistant-request work item and a reply uses bot identity.
- Given another human mentions the assistant bot in a group or privately
  messages it, when the message is observed, then no work item, working
  reaction, model run, or reply is produced.
- Given an older version already persisted and approved a bot reply for a
  non-owner assistant mention, when the upgraded daemon resumes that exact
  approval, then the final sender-identity gate blocks the reply, marks the
  approval blocked, and completes the work without sending.
- Given a non-owner privately messages the assistant bot, when routing runs,
  then the runtime ignores it before any model call.
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
- Given a direct status update, task handoff, or coordination request, when the
  model can complete a bounded relevant read, then it replies with the checked
  facts, explicit unknowns, and concrete information passed to the owner rather
  than merely acknowledging or restating the request.
- Given a delegated reply is ready, when it is rendered for Lark, then it names
  the intelligent assistant, includes useful completed work, and says that the
  configured owner name was notified.
- Given the configured language is Chinese, when a model reason or provider
  error is English, then owner and lifecycle messages use a Chinese summary and
  do not paste the English paragraph.
- Given language is automatic and the bounded conversation is predominantly
  Chinese, when a reply or owner notice is rendered, then its explanatory prose
  is Chinese; an inconclusive conversation uses the configured fallback.
- Given the owner name is unavailable, when delegated rendering is attempted,
  then the action fails explicitly with a configuration instruction and never
  substitutes the generic word "user".
- Given a delegated owner message and bounded evidence cannot support a
  specific factual answer without exposing private context or inventing work,
  when it chooses a terminal action, then it sends a concise response stating
  the completed check and exact unknown or refusal, or requests approval for an
  exact risky response; it cannot silently record or notify.
- Given any reply has confidence below `policy.reply_confidence_min`, when the
  final gate runs, then direct owner mentions and assistant-facing requests
  enter approval just like other replies and do not use a hidden lower floor.
- Given a coding question has read a production source that supports only part
  of a multi-field question, when the next model turn starts, then that read is
  citable evidence but does not by itself prove the investigation complete or
  remove the remaining bounded code tools.
- Given code-index or workspace search returns a candidate production path,
  when the model has not yet read that file with `read_workspace`, then the
  candidate source does not trigger convergence and the production read remains
  available.
- Given the model cites only code-index or workspace-search candidate sources,
  when it submits a definite coding claim without an authoritative production
  read, then the terminal decision is rejected and the bounded investigation
  continues.
- Given a search-only coding reply uses an ordinary requirement phrase such as
  "需要返回", when it still makes a definite code claim, then that phrase does
  not misclassify the reply as an explicit unknown or bypass the authoritative
  read requirement.
- Given exact bounded searches do not find a named symbol, when the model
  declares `evidence_status=insufficient`, then the runtime emits the canonical
  evidence-limited answer without inventing a production source.
- Given an `insufficient` reply mixes an unknown phrase with an unsupported
  definite inference, when the terminal decision is accepted, then the runtime
  discards the free-form text and only the canonical evidence-limited answer is
  sender-visible.
- Given a coding question has performed no workspace/code investigation, when
  the model immediately submits `evidence_status=insufficient`, then the
  runtime rejects it and requires a bounded relevant code search or read before
  accepting the canonical evidence-limited answer.
- Given the current work is a `coding_question`, when the model attempts to
  finish with `ignore`, `record`, `notify`, or `request_approval`, then the
  runtime rejects that terminal path; code fact questions must finish as an
  evidence-verified reply or a canonical evidence-limited reply and cannot
  disappear without a sender-facing answer.
- Given the model submits a `reply` without `reply_confidence`, when the terminal
  decision is parsed, then the decision is rejected for bounded model repair
  instead of entering approval as a zero-confidence reply.
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
- Given the assistant bot replies to an owner-request private message or an
  allowed owner-authored group assistant mention, when the message is posted to
  Lark, then it uses bot identity and does not add the owner-delegation `🤖`
  marker.
- Given the group reply is blocked, cancelled, awaiting approval, or fails, when
  reply execution stops, then the owner notice must not claim that the agent
  already replied or will send without approval.
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
- Given any sender asks the assistant to inspect credentials, enumerate the
  machine environment, or read an explicit path outside the configured
  workspace, when request handling starts, then no evidence tool executes and
  the sender receives a concise refusal that accepts only a concrete business
  question.
- Given a non-owner triggers a delegated owner mention, when the model catalog
  and registry are evaluated, then shell, cross-chat search, and every
  owner-only or side-effect tool are unavailable and remain denied even if the
  model constructs a direct tool call.
- Given a non-owner calls `get_lark_context` with a chat ID other than the
  source chat, when the registry authorizes the call, then it rejects the call
  before the Lark provider executes.
- Given adjacent context contains unrelated app/bot deployment messages, when
  it is compacted, then those messages are excluded while the human target and
  explicitly referenced app/bot parent or root messages remain.
- Given a delegated coordination or investigation request, when the proposed
  reply only says the owner was reminded or repeats the request without a
  successful relevant read receipt, then the terminal quality gate rejects it.
- Given a delegated run re-reads the same context digest or receives an empty
  or unreadable image, when relevant-work receipts are counted, then those
  calls do not satisfy the completed-investigation requirement.
- Given the delegated run reads relevant same-chat or production workspace
  evidence, when the reply briefly states completed work, an initial finding or
  explicit unknown, and what was passed to the owner, then it may pass the
  quality gate.
- Given a coding reply cites only an example, test, fixture, or documentation,
  when it claims definite production behavior, then verification rejects it
  until production source is cited or the reply states the production unknown.
- Given a simple assistant request uses its first two model turns for bounded
  search and narrowed production-source reads, when the second tool batch
  completes, then the default third model turn remains available for
  `submit_decision` instead of failing the work item at the old two-turn limit.
- Given a non-owner-triggered automatic reply says the owner or team will later
  deliver, coordinate, or report back, when no exact approval exists, then the
  terminal quality gate rejects the commitment before send.
- Given shell approval is disabled, when a workspace command runs, then it
  executes and is audited; given approval is enabled and the command is risky,
  then it waits durably and resumes only after owner approval.
- Given the daemon crashes during a read-only tool, when it restarts, then the
  work item is interrupted at that tool stage and startup convergence readmits
  it with current evidence; given it crashes during shell or IM execution, then
  the action becomes uncertain, is not repeated, and the work is terminalized
  with an owner reconciliation notice.
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
  executing, when the daemon starts, then it first becomes `interrupted` with
  its last durable model/tool/action stage; after ready, startup convergence
  readmits only the safe items.
- Given the owner runs `queue inspect` for a message, when state is returned,
  then it states whether the message was observed, admitted, replied,
  interrupted, or suppressed and includes any uncertain external action.
- Given the owner explicitly resumes one interrupted or offline-backlog
  message, when the current session claims it, then a new run uses current
  evidence while preserving the prior audit timeline.
- Given the configured owner privately sends `/help`, when routing runs, then
  the bot returns localized query and mutation commands without a model call.
- Given a non-owner privately sends `/tasks` or mentions the assistant with it,
  when intake runs, then no work, reaction, model call, or reply is created.
- Given the owner sends a control command in a group, when routing runs, then
  only a private-control redirect is returned and no queue detail is exposed.
- Given no work needs owner attention, when `/tasks` runs, then it says there
  is nothing to handle and does not expose zero-valued raw status maps.
- Given actionable work exists, when `/tasks` renders a page, then every item
  explains the latest durable evidence and includes an exact valid next
  command.
- Given a mutation command names an invalid ID or ineligible transition, when
  it runs, then it changes nothing and returns the exact safe alternative.
- Given externally uncertain work, when retry, resume, or cancel is requested,
  then it is rejected until the owner records an explicit reconciliation.
- Given a command transaction committed but its reply was not delivered, when
  the same command message is processed again, then the stored response is
  resent and the state mutation is not repeated.
- Given the daemon briefly holds the SQLite writer slot, when an owner mutation
  begins and the writer slot is released within the configured wait interval,
  then the mutation waits, commits once, and does not fail by upgrading an old
  read snapshot.
- Given a resolved terminal task is explicitly resumed, when that newer work
  epoch later becomes interrupted or terminal again, then the previous
  resolution remains audit history and `/tasks` shows the task as actionable.
- Given an actionable task contains an internal Lark mention placeholder, when
  `/tasks` or `/task <id>` renders it, then known mentions use the stored display
  name, unknown mentions use a localized generic label, and no `@_user_`
  placeholder reaches the reply action.
- Given an operator has audited interrupted historical work, when
  `queue cancel --all-interrupted` includes explicit kept work IDs and a
  non-empty reason, then every other safe interrupted item becomes cancelled,
  unsent approvals are cancelled, unresolved interruption snapshots are
  closed, and a durable operator-reason action is recorded without deleting
  history or sending a message.
- Given any selected cancellation item is running or has an executing or
  result-uncertain external action, when cancellation is requested, then the
  whole batch fails without changing any selected work.
- Given an approval belongs to an older session and a newer daemon session is
  ready, when the owner approves the exact action, then its work is assigned to
  the newer session as received; given the newest session is still starting or
  no session is active, the approved action remains ready while the work stays
  interrupted until the next ready startup convergence assigns it.
- Given user-token polling was unavailable and the owner later authorizes it,
  when the owner runs `queue backfill` with a bounded chat and time range, then
  only matching @Owner messages are recorded through normal intake and the
  normal poll cursor is not advanced.
- Given a reply send was in flight when the process stopped, when recovery
  cannot prove whether Lark accepted it, then the action remains uncertain, the
  reply is not resent, and one separately fenced reconciliation notice is
  attempted.
- Given tool budget, no-progress, or citable evidence forces a terminal
  decision, when the model calls an earlier investigation tool and then submits
  a valid decision, then the earlier call is rejected without execution and
  the decision completes; when the model ignores terminal-only instructions
  for three attempts, then the runtime performs one no-tool terminal finalizer
  request over the retained tool receipts. If that finalizer produces a valid
  typed decision, the work completes with the same validation and send gates as
  `submit_decision`; if it fails validation, the work moves to dead letter and
  the Owner summary lists only successful runtime-recorded tool checks plus the
  finalizer failure reason.
- Given coding work is configured, when a coding question enters the model
  loop, then its default model-turn ceiling is high enough for deeper
  investigation while every prompt still reports current turn, maximum turns,
  remaining turns, tool budget, and encourages the model to converge in fewer
  turns whenever evidence is sufficient.
- Given a coding run proves one requested fact but cannot prove another after
  bounded repair, when it submits `reply_outcome=partial`, then every definite
  claim remains grounded, the exact unknown and next step remain visible, and
  the whole investigation is not re-run through the general retry budget.
- Given the current coding-classified input is unreadable or lacks the path,
  content, attachment, or referent required to investigate, when the model
  submits `reply_outcome=clarification`, then it completes without a fake code
  read or generic failure notice, and free-form code claims are replaced with
  a deterministic statement of unknowns and the required input.
- Given the same normalized tool call returns the same stable success or
  deterministic failure,
  when the condition does not change, then the second identical occurrence
  asks for a changed strategy and the third closes broad investigation so the
  next request exposes only `submit_decision`; it never creates 20 complete
  model runs.
- Given a model ignores prompt instructions and attempts a forbidden,
  over-budget, or unsupported action, when the call reaches the Go harness,
  then deterministic policy still rejects it and only a safe typed terminal
  outcome may proceed.
- Given semantic context remains incomplete or ambiguous through the configured
  retry ceiling, when the final exact lease is deferred, then the work moves
  atomically to dead letter, has no future attempt time, produces one localized
  owner summary with `/task <id>` and any safe unsent candidate or progress,
  and does not send a late message to the original chat.
- Given provider retry configuration is lower than the semantic retry ceiling,
  when one semantic owner-context defer occurs, then it increments only
  `owner_reply_retry_count`, leaves provider `retry_count` unchanged, and
  remains `waiting_user` until its own ceiling is reached.
- Given a validated delegated reply candidate and an ambiguous final owner
  check, when later checks remain ambiguous and then become `unanswered`, then
  the main model was called once, the exact candidate survived every defer, and
  it is sent once after the Owner notice.
- Given a held candidate and a later check finds the Owner answered or the
  source message was withdrawn, when the work is reclaimed, then the candidate
  is cancelled and no sender-facing reply is sent.
- Given a held candidate and routing policy is paused or newly blocks its chat
  or sender, when the work is reclaimed, then current deterministic routing
  cancels the candidate before semantic resolution or any sender-facing send.
- Given an expired worker attempts to create, hold, consume, or cancel a
  validated candidate, when its lease token no longer owns the processing work
  item, then the transition fails and cannot alter the current run.
- Given a v15 database has a `waiting_user` row whose semantic retries were
  stored in `retry_count`, when v16 migration runs, then those retries move to
  `owner_reply_retry_count` and the provider counter resets to zero.
- Given a candidate saved before a crash, duplicate intake, or an explicit
  terminal resume, when recovery runs, then candidate state, reply idempotency,
  cancellation, and terminal-generation behavior remain consistent without
  replaying an external action.
- Given the process stops after the dead-letter transaction commits but before
  its owner-summary action begins, when the daemon continues or restarts, then
  the same-transaction owner-resolution requirement is discovered and consumed
  once; a failed first send is retried from its durable blocked action, while
  an executing result-uncertain send is never replayed.
- Given terminal work is explicitly resumed before its pending owner summary
  begins, when maintenance next runs, then the old terminal-generation
  requirement is cancelled and no stale "work stopped" summary is sent.
- Given a terminal owner summary has a known failed send result, when the Owner
  explicitly resumes that work, then the failed summary action is cancelled as
  superseded; an executing summary whose Lark result is uncertain still blocks
  resume and is never replayed.
- Given a completed terminal work is explicitly resumed and later reaches dead
  letter again, when the second terminal generation is committed, then it gets
  a new outbox identity and a new idempotent owner summary; historical
  owner-resolution actions from the first generation cannot complete it.
- Given the user intentionally stops or restarts the service, when control
  begins, then the bot sends one idempotent private offline notice before
  `launchctl` unloads it; an unexpected crash sends no false offline notice.
- Given configuration, recovery snapshot, intake creation, and startup
  convergence are ready, when any new daemon process session comes online, then
  the bot sends one idempotent private online notice with resumed,
  waiting-owner, terminalized, and uncertain counts.
- Given every lifecycle count is zero, when an offline or online notice is
  rendered, then it says there is no unfinished or actionable work and does
  not enumerate zero-valued categories.
- Given lifecycle counts contain non-zero categories, when a notice is
  rendered, then it includes only those categories, explains their meaning,
  and points to `/tasks` or the exact handling command.
- Given a reply decision, when policy blocks, awaits approval, sends, or finds
  the owner already replied, then the durable action status records that exact
  outcome and no generic fallback text is sent.
