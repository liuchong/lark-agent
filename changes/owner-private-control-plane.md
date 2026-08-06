# Owner private control plane

## Goal

The configured owner can understand and finish durable agent work from the
assistant's private Lark chat. Every reported item explains what it means,
what evidence is known, and the exact safe command that can advance it.
Lifecycle notices stop reporting meaningless zero-valued categories.

## Business design

### Command catalog

Read-only commands are:

- `/help [command]`
- `/status`
- `/doctor`
- `/tasks [action|running|recent|all] [page]`
- `/task <work-id>`
- `/approvals [page]`
- `/approval <action-id>`
- `/recent [count]`
- `/version`
- `/ping`

Mutation commands are:

- `/task retry <work-id>`
- `/task resume <work-id> [confirm]`
- `/task cancel <work-id> <reason>`
- `/task acknowledge <work-id> <note>`
- `/task reconcile <work-id> completed|not-completed|unknown <reason>`
- `/approval approve <action-id> confirm`
- `/approval reject <action-id> <reason>`

Chinese query aliases are accepted, but every response shows the canonical
slash command. Free-form natural language cannot mutate queue or approval
state.

### Identity and visibility

- Commands are authorized only when the configured owner sends them in the
  assistant bot's private chat.
- An owner command sent in a group receives only a private-control redirect and
  does not reveal queue details.
- A non-owner private message or native assistant mention remains silent.
- Delegated-reply work triggered by another human cannot obtain the command
  service or mutate owner state.

### Task query model

The default `/tasks` view is `action`. It includes only work requiring an owner
decision or reconciliation and excludes completed, ignored, cancelled, and
acknowledged history. Views are bounded to ten items per page.

Each rendered task contains:

- stable work ID and a sanitized source summary;
- a localized semantic state rather than the raw storage status;
- the last durable fact, failure, or uncertainty;
- why owner attention is or is not required;
- one or more exact currently allowed commands.

`/task <work-id>` additionally shows the latest durable stage, approval or
external-action evidence, and every valid next action. It never exposes model
chain-of-thought, credentials, raw events, open IDs, unrestricted absolute
paths, or full internal JSON.

### State decisions

- `retry` is allowed only for current-session `retry_wait` work with no
  executing, blocked, or uncertain external action.
- `resume` is allowed for safe non-terminal interrupted or offline work.
  Replaying terminal history requires `confirm`.
- `cancel` requires an exact work ID and non-empty reason. It is rejected for
  running work, terminal work, executing actions, and unresolved uncertain
  external actions.
- `acknowledge` records an owner resolution overlay without deleting or
  rewriting historical work evidence. Acknowledged work leaves the default
  action view.
- A resolution stores the exact `work_updated_at` snapshot it closes. Explicit
  resume advances the work update epoch, so a later interruption or terminal
  failure becomes actionable again without deleting old resolutions or
  ordering variable-precision timestamp text.
- `approve` requires `confirm`; `reject` requires a non-empty reason.
- Unknown or ineligible operations fail without mutation and explain the
  exact safe alternative.

### External-result reconciliation

An uncertain external action is never replayed by a command.

- `completed` records that the owner verified the action happened and closes
  the work without replay.
- `not-completed` records that the owner verified the action did not happen,
  resolves the uncertainty, and leaves any later resume as a separate explicit
  command.
- `unknown` records the review, keeps the work terminal and non-replayable, and
  explains what must be checked outside the agent.

Every reconciliation stores the owner reason, source command message, resolved
action, and timestamp.

### Durable command execution

The router only parses and classifies a command. It does not query or mutate
storage during intake classification.

After the ordinary durable work claim, a control service executes the typed
command before any model call. Its result is sent through the existing durable
reply-action path. The same Lark command message is idempotent: after a crash
or reply failure, reprocessing returns the stored result and never repeats the
state mutation.

Schema version 11 adds:

- an owner-command journal keyed by command message identity, storing parsed
  command, execution status, response, and failure;
- owner work resolutions for acknowledge and reconciliation, preserving the
  original work and action history;
- indexes for actionable-work and pending-approval pagination.

Schema version 12 adds the exact `work_updated_at` snapshot to owner work
resolutions. Existing version-11 rows keep a null snapshot and therefore
re-enter explicit owner review instead of hiding a possibly newer work epoch.

Migration is additive. Existing rows remain valid. Existing unresolved
terminal work enters the action view until the owner acknowledges or
reconciles it.

The idempotency-journal insert is the mutation transaction's first SQL
statement. Duplicate message IDs use `ON CONFLICT DO NOTHING` and then read the
stable committed result, avoiding a deferred read-to-write upgrade window.

### Shared business service

Bot commands and local CLI commands use the same typed query, transition, and
recommendation service. The CLI keeps structured stdout; the Lark presenter
renders localized human text. Help text is generated from one command catalog
so `/help`, detailed help, CLI `--help`, and documentation cannot silently
diverge.

### Lifecycle rendering

Offline and online notices include only non-zero categories. If every category
is zero, the notice says there is no unfinished or actionable work and points
to `/help`. A non-zero category explains its meaning and points to `/tasks` or
the exact follow-up command.

Configured language wins. Automatic language follows the bounded conversation;
lifecycle notices use the configured fallback language. One notice never
mixes Chinese and English explanatory prose.

### Failure behavior

- Query failures return a localized bounded error and a diagnostic command.
- Validation and authorization fail before storage mutation.
- A committed command whose Lark reply fails remains durably replyable.
- SQLite busy handling follows the existing bounded retry policy.
- Unknown slash commands return help without entering the model.

### Configuration and installation

No new secret or mandatory configuration is added. Existing owner identity and
language settings control authorization and rendering. Installation upgrades
the SQLite schema automatically and otherwise preserves current settings.

## BDD acceptance

- Given the configured owner privately sends `/help`, when routing runs, then a
  localized command catalog is returned without a model call.
- Given a non-owner privately sends `/tasks` or mentions the assistant with it,
  when intake runs, then no work, reaction, model call, or reply is created.
- Given the owner sends a control command in a group, when routing runs, then
  only a private-control redirect is returned and no queue detail is exposed.
- Given no work needs attention, when the owner sends `/tasks`, then the reply
  says there is nothing to handle and does not print zero-valued status maps.
- Given actionable work exists, when `/tasks` renders a page, then every item
  explains its state and contains an exact valid next command.
- Given an exact work ID, when `/task <id>` runs, then the reply contains the
  latest durable evidence and omits secrets, raw events, and model reasoning.
- Given an invalid ID or ineligible transition, when a mutation command runs,
  then it changes nothing and names the safe alternative.
- Given current-session retryable work, when `/task retry <id>` runs, then it
  becomes immediately claimable exactly once.
- Given terminal work, when resume lacks `confirm`, then it is rejected without
  mutation.
- Given uncertain external work, when retry, resume, or cancel is requested,
  then it is rejected until an explicit reconciliation is recorded.
- Given the owner reconciles an action as completed, not completed, or unknown,
  when the command commits, then the corresponding non-replay state and audit
  evidence are durable.
- Given a command transaction committed but its reply was not delivered, when
  the same command message is processed again, then the stored response is
  resent and the mutation is not repeated.
- Given every lifecycle count is zero, when offline or online rendering runs,
  then it emits one concise localized sentence instead of all zero categories.
- Given some lifecycle counts are non-zero, when rendering runs, then only
  those categories and their exact handling path appear.

## Tests and fixtures

- Parser and presenter tests cover aliases, quoting, pagination, localization,
  sensitive-field filtering, unknown commands, and detailed help.
- Storage tests migrate a version-10 database and exercise command
  idempotency, action pagination, acknowledgement, reconciliation, and atomic
  rejection.
- Application tests prove control execution occurs after durable claim and
  before any model call.
- Integration fixtures include each actionable status, pending approvals,
  executing and uncertain actions, duplicate command messages, and Chinese and
  English owner profiles.
- Lifecycle regression tests cover all-zero and mixed non-zero summaries.

## Documentation

`spec/behavior.md`, `README.md`, `docs/operations.md`,
`docs/install-macos.md`, CLI help, and bot `/help` describe the same commands
and safety rules. A reusable implementation lesson is recorded under
`.agents/experience/`.

## Non-goals

- No arbitrary shell, SQL, log, secret, environment, or configuration browser.
- No daemon update, installation, restart, or destructive history deletion
  through Lark commands.
- No free-form natural-language task mutation.
- No non-owner control path or group-visible operational detail.
- No automatic replay or guessed reconciliation of uncertain external actions.
- Interactive Lark cards remain a future presentation option and are not
  exposed in help or tests.
