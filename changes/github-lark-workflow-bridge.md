# GitHub Workflow And Lark Conversation Bridge

## Goal

Connect trusted GitHub workflow facts to an ongoing Lark conversation without
creating a second long-running Lark event consumer. A short-lived GitHub Action
sends a bot-authored notification. The installed daemon remains the only
WebSocket consumer, verifies quoted notification references, and may fetch
fresh, bounded, read-only GitHub context for an evidence-backed reply.

The bridge is generic. Repository code and committed examples contain no
personal name, production bot name, or production chat identifier.

## Existing Behavior That Must Remain

- Only the configured owner may privately invoke the assistant or natively
  mention it in a group.
- A non-owner direct assistant mention or private message is silent before
  queueing and model work.
- A non-owner may trigger the delegated workflow only by mentioning the
  configured human owner in an allowed group, and that invocation is read-only.
- `assistant.reply_scope` and `policy.reply_scope` remain independently
  configurable as `all_groups` or `configured_groups`.
- Workspace escape and descriptive host or credential reconnaissance are
  rejected before evidence tools execute.
- Useful delegated replies require real relevant work and cannot be empty
  acknowledgements or invented future commitments.

## Process And Identity Model

There are two process roles:

1. `lark-agent github notify` is a short-lived HTTP-only sender. It reads one
   trusted GitHub event and bounded GitHub API data, renders one deterministic
   Lark post, sends it with bot identity, prints a typed result, and exits.
2. `lark-agent daemon run --live` is the single long-running Lark event
   consumer. It receives a human reply, resolves the Lark reply chain, verifies
   the bot-authored GitHub reference, persists that reference, and exposes a
   bounded read-only GitHub evidence tool to the model.

Both roles may use the same Lark application credentials. The Action must not
start a WebSocket connection. Multiple WebSocket consumers for the same app are
not used because event delivery may be distributed between connections.

## GitHub Reference Model

`GitHubReference` is versioned trusted control data:

- `schema_version`
- `repository` in canonical `owner/name` form
- `kind`: `workflow_run` or `pull_request`
- `workflow_run_id` and `workflow_run_attempt`
- optional `pull_request_number`
- `head_sha`
- `html_url`

The notification contains a canonical machine-readable reference marker in a
low-noise footer. The marker carries an HMAC-SHA256 signature derived from the
same Lark app secret held by the Action and the installed daemon. The secret is
never included in the marker. The marker is untrusted until all of these checks
pass:

- the marker schema and all identifiers are valid;
- the marker signature verifies with the configured current Lark app secret;
- the referenced repository is in `github.allowed_repositories`;
- the Lark message sender type is `app`;
- the sender ID equals the configured current Lark app ID;
- the message belongs to the same chat and quoted/root chain as the request.

A marker authored by a human, another app, an ordinary bot reply induced to
repeat marker-shaped text, or an untrusted adjacent message does not create
GitHub authority. An invalid signature leaves the GitHub tool unavailable
without blocking the ordinary Lark answer. When multiple trusted references conflict,
the nearest direct trusted parent wins only if it is internally consistent;
otherwise the GitHub tool remains unavailable.

## Durable State

SQLite schema migration 9 adds `external_references`:

- provider and kind
- Lark message ID and chat ID
- sender app ID
- canonical external key
- canonical reference JSON and digest
- verified and updated timestamps

`provider + lark_message_id` is unique. Persisting an identical reference is
idempotent. Reusing one Lark message ID with conflicting reference content
fails closed. The daemon may lazily verify and persist an older bot notification
when a human first replies to it; no eager historical migration or replay is
performed.

Only the verified reference is retained in this table. GitHub snapshots and
tool results follow existing bounded run/audit retention instead of creating a
second long-lived cache.

## GitHub HTTP Boundary

`internal/github` owns typed GitHub REST requests and event decoding. It uses
the standard library HTTP client, accepts GitHub Enterprise-compatible API base
URLs, validates all successful response shapes, and returns typed partial,
forbidden, rate-limited, not-found, and invalid-data failures.

The notification command accepts `workflow_run` and pull-request event
envelopes from `GITHUB_EVENT_PATH`. It may enrich them with:

- workflow run and jobs/steps;
- check annotations;
- pull request metadata and changed-file summaries;
- reviews and review state.

It never checks out or executes pull-request code, interpolates event text into
shell, downloads artifacts, or retrieves raw job logs. Untrusted titles,
branches, annotations, and review text are data only.

Model tool arguments select bounded sections such as `summary`, `checks`,
`files`, or `reviews`. They never select repository, pull request, run ID, API
base URL, or credentials. Those values come from the verified invocation
reference and validated local configuration.

## Notification Contract

The notification is a deterministic Lark `post` containing status, repository,
pull request or workflow run, commit, failed jobs/steps, bounded annotations,
and GitHub links. It explicitly marks partial enrichment.

The Lark message UUID is a stable digest of destination chat, repository,
workflow run, attempt, and notification schema and is at most 50 characters.
Retries reuse the same UUID. If delivery is uncertain, the command returns
nonzero so the workflow may retry with that same key.

Standard output contains only one JSON result with message ID, chat ID,
reference, partial state, and idempotency key. Progress, warnings, and errors
go to standard error. Secrets and authorization headers are never included.

## Configuration And Credentials

Configuration version 4 adds:

```yaml
github:
  enabled: false
  api_base_url: https://api.github.com
  token_keychain_service: lark-agent
  token_keychain_key: github_token
  allowed_repositories: []
  max_files: 50
  max_patch_bytes: 65536
  max_annotations: 50
  max_reviews: 50
```

Generic defaults keep the bridge disabled. Enabling it requires at least one
allowed repository. Existing version 3 configurations remain loadable.

Local daemon access uses a read-only GitHub token from macOS Keychain. GitHub
Actions use the current repository `GITHUB_TOKEN` for GitHub reads and a
protected GitHub Environment secret for the Lark app secret. Secret values are
environment-only and have no command-line flag.

The Action requires an explicit Lark OpenAPI base URL instead of inheriting the
official SDK's Feishu default. This keeps one generic Action usable for both
`https://open.larksuite.com` and `https://open.feishu.cn` without silently
sending an international app credential to the wrong domain.

The production installation explicitly keeps both Lark reply scopes at
`all_groups` and enables only the intended GitHub repositories. Production chat
destinations remain private installation or GitHub Environment values.

## Failure And Recovery

- A malformed event or missing required identifier fails before Lark send.
- An enrichment failure sends a clearly marked partial notification only when
  the trusted event itself contains enough facts; otherwise the command fails.
- Missing or invalid Lark credentials fail before message creation.
- Missing GitHub credentials disable the local GitHub tool without disabling
  existing Lark intake and replies; doctor reports the exact boundary.
- Rate limits, forbidden responses, and not-found responses become explicit
  evidence states. They never produce guessed facts.
- An absent or untrusted quoted reference leaves the GitHub tool unavailable.
- Restart recovery loads an already verified reference without rerunning or
  replaying its original notification.

## Permission And Silence Rules

- Owner assistant requests may use the GitHub read tool only when their quoted
  chain contains a verified reference.
- Non-owner direct assistant mentions and private messages remain silent even
  inside a GitHub notification thread.
- A non-owner who mentions the human owner in that thread may receive a useful
  delegated response, but the GitHub tool remains read-only and all existing
  non-owner mutation, shell, cross-chat, and reconnaissance restrictions apply.
- No GitHub merge, comment, rerun, cancel, delete, edit, or arbitrary repository
  search tool is exposed.

## BDD Acceptance

### Same app, separate process roles

Given a trusted workflow completes, when the HTTP-only Action sends a
notification with the installed app credentials, then the daemon remains the
only WebSocket consumer and receives a human reply to that same bot identity.

### Idempotent workflow notification

Given one workflow run and attempt is delivered more than once, when the Action
retries, then every attempt uses the same valid Lark UUID and produces at most
one notification.

### Untrusted pull-request input

Given a fork pull request contains shell text, a fake reference marker, or
prompt injection in its title and annotations, when the trusted notification
workflow runs, then the content is rendered only as data and no pull-request
code, artifact, log, or event-derived shell command executes.

### Owner evidence-backed follow-up

Given the owner replies to a current-app notification in any allowed group,
when the daemon verifies the reference and the model requests GitHub context,
then fresh bounded API facts are returned with source references and the reply
states supported findings or explicit unknowns.

### Existing non-owner silence and delegated read-only behavior

Given a non-owner directly mentions or privately messages the assistant in a
notification thread, when intake runs, then no work item, tool call, reaction,
or reply is produced.

Given a non-owner mentions the human owner in that thread, when delegated work
runs, then it may read the verified GitHub reference and same-chat context but
cannot execute shell or any GitHub/Lark/workspace mutation.

### Spoofed and conflicting references

Given a human or another app posts a syntactically valid GitHub marker, or a
reply chain contains conflicting trusted markers, when context is built, then
the GitHub tool remains unavailable and no external repository is queried.

### Restart and truthful failures

Given a verified reference was persisted, when the daemon restarts and handles
a later reply in the same chain, then it loads the same reference without
replaying the Action notification.

Given the referenced PR or workflow does not exist, credentials are missing,
or GitHub returns forbidden or rate limited, when the model asks for evidence,
then the result identifies that exact limitation and the reply does not invent
facts.

### Bounded data

Given a pull request exceeds configured file, patch, annotation, or review
limits, when notification or follow-up enrichment runs, then output is
truncated deterministically, marks the result incomplete, and reports every
omitted count that can be proven from the fetched API response. It never invents
an exact count for unseen pagination.

Given GitHub returns HTTP 200 with missing or contradictory required fields,
when the typed boundary decodes it, then it fails as invalid data rather than
converting the response into an empty successful result.

Given a GitHub Enterprise API base URL contains a path prefix such as
`/api/v3`, when a trusted reference is fetched, then every repository endpoint
preserves that prefix.

## Test Locations

- `internal/github/*_test.go`: event parsing, typed API data, hostile text,
  partial enrichment, limits, rate limiting, forbidden and not-found responses.
- `internal/lark/im_test.go`: arbitrary bot post send, typed result, UUID, and
  no WebSocket side effect.
- `agent/storage/*_test.go`: migration 9, idempotent trusted reference storage,
  conflicts, and restart reads.
- `agent/context/*_test.go`: current-app marker verification, spoof rejection,
  conflicting chains, and bundle projection.
- `agent/tools/github_test.go` and permission tests: reference-bound arguments,
  read-only execution, repository allowlist, and non-owner scope.
- `integration_test/lark_agent/github_lark_bridge_test.go`: dry-run command,
  two-process fake end-to-end flow, owner/non-owner/spoof/restart behavior.
- `integration_test/lark_agent/help_contract_test.go`: root and detailed GitHub
  help synchronization.

Fixtures use synthetic repositories, app IDs, chat IDs, message IDs, API
responses, and hostile text. No production identifier or secret is committed.

## Documentation And AI-Facing Records

Update `spec/behavior.md`, `spec/architecture.md`, `spec/lark-sdk-boundary.md`,
`README.md`, `docs/configuration.md`, `docs/operations.md`,
`docs/development.md`, command help, and one reusable `.agents/experience/`
record for the one-listener and app-authored-reference trust boundary.

## Non-Goals

- A second Lark WebSocket consumer.
- GitHub mutation or arbitrary repository discovery.
- Pull-request code, artifacts, or raw log execution.
- Interactive-card actions.
- Hard-coded production people, bots, chats, repositories, or credentials.
- Automatic replay or migration of historical Lark work.
