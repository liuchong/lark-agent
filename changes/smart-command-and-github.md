# Smart Commands And Built-In GitHub Support

This round's design note. The long-lived testable contract is
[`spec/smart-command.md`](../spec/smart-command.md) (`SC-*`, `GW-*`, parse
fixtures, HTTP allow/deny, JSON). If this file and that spec diverge after
merge, `spec/smart-command.md` wins.

## Goal

Add **smart commands**: the same agent main loop, started for one job, with no
Lark WebSocket, using on-demand HTTP (short connections) for Lark when a tool
needs it, then exiting. Contrast **ordinary commands**, which compose primitive
capabilities in fixed code and do not run a model. `github notify` stays an
ordinary command.

On top of smart commands, add **built-in GitHub support**: read GitHub events,
read repository content, and load GitHub Actions secrets and variables. Without
this layer the runtime cannot see GitHub events, repository files, or Actions
secrets/vars.

**GitHub example workflows** are committed workflow YAML plus prompt/rule files
that call that GitHub support. They demonstrate usage. They are not a substitute
for the GitHub support itself.

Committed files use only synthetic identifiers. Production chat IDs, app
secrets, and model keys stay in GitHub Environment secrets and variables.

## Existing Behavior That Must Remain

- `lark-agent github notify` remains HTTP-only, deterministic, and must not
  start a WebSocket.
- The installed daemon remains the only long-running WebSocket consumer.
- Daemon owner/non-owner silence, delegated read-only replies, and workspace
  escape rejection are unchanged.
- Quoted GitHub notification markers stay trusted only after current-app HMAC
  verification. Smart-command GitHub tools use the **current event reference**,
  not a quoted Lark marker, unless a tool is explicitly reading Lark context.
- Public-repository safety: no real people, chats, tenants, or secrets in the
  tree.

## Vocabulary

| Term | Meaning |
|---|---|
| Smart command | Agent main loop, no long connection, exit when done |
| Ordinary command | Fixed primitive composition, no model |
| Built-in GitHub support | Event, repo, token, Actions secrets/vars, mention parse |
| GitHub example workflow | YAML + prompts using that support |

Do not call this feature a short-lived nickname, a lightweight agent, or
"GitHub-only agent". GitHub is the first built-in integration, not a nickname
for smart commands.

## Commands

### Smart command entry

`lark-agent run` starts a smart command:

- does not create a Lark WebSocket client;
- requires a model API key from Keychain or `OPENAI_API_KEY` (same env fallback
  as daemon model login);
- takes `--prompt-file`, optional `--rules-file`, `--allowed-actions` (comma
  separated), optional `--message` (inline user text), optional `--chat-id`,
  `--dry-run`;
- uses a process-local SQLite file (temp dir unless `--state` is set) and does
  not resume daemon investigations;
- prints one JSON object on stdout and exits 0 on success, non-zero on failure;
- writes progress and warnings to stderr.

`--allowed-actions` is the only write-capability list. Unknown names fail
before the model runs. An empty list means read-only tools only.

### Built-in GitHub entry

`lark-agent github run` is built-in GitHub support wrapping `run`:

- requires `GITHUB_EVENT_PATH` and `GITHUB_EVENT_NAME` (or `--event-path` /
  `--event-name`);
- when `GITHUB_ACTIONS=true`, assembles config like `github notify` (app id,
  base URL, current `GITHUB_REPOSITORY` allowlist, `GITHUB_TOKEN`);
- requires `LARK_AGENT_LARK_BASE_URL` in Actions;
- parses the event into a typed reference; unsupported event names fail before
  the model;
- if the event is `issue_comment` or `pull_request_review_comment`, parses
  `@lark-agent` (see Comment Grammar);
- injects the typed event summary into the agent context by default; the model
  may ignore it, but the runtime always loads it;
- repository, issue/PR number, SHA, run id, API base URL, and credentials come
  from the event and config, never from model tool arguments;
- prompt and rules files are read only from the checked-out workspace (trusted
  default-branch checkout in Actions).

`lark-agent github notify` is unchanged.

### Action

[`action.yml`](../action.yml) `mode` input:

- `notify` (default): current Docker args `github notify --chat-id ...`
- `run`: args `github run` plus prompt/rules/allowed-actions/chat-id

[`Dockerfile`](../Dockerfile) `ENTRYPOINT` is `/usr/local/bin/lark-agent` with
no hardcoded subcommand.

## Comment Grammar

Apply only when the GitHub event is an issue or review comment.

1. If the comment body does not contain `@lark-agent` (ASCII, case-insensitive
   as a whole token), GitHub support must not start a smart command. Workflows
   should also `if:` skip. If the command is still invoked, it exits 0 with
   `skipped=true` and does not run the model (`SC-17` in
   `spec/smart-command.md`), so a looser workflow `contains()` cannot fail CI.
2. Take the text after the first `@lark-agent`.
3. If the next token matches `^/[a-z][a-z0-9-]*$`, it is a slash command.
4. Then consume tokens matching `^--[a-z][a-z0-9-]*$` or
   `^--[a-z][a-z0-9-]*=.*$`. First release accepts only `--dry-run` (bare flag).
   Any other `--` token fails before the model: stderr explains the allowed
   flags; stdout is empty; exit non-zero. No GitHub comment is posted for a
   flag parse error.
5. Remaining text, including later lines, is `extra_prompt`.
6. `@lark-agent review` (no slash) is **not** a command. The word `review` is
   part of `extra_prompt`.
7. Known slash commands: `/review`, `/title`, `/check`. No `/ask`,
   `/summarize`, `/changelog`, `/notify`.
8. Unknown `/foo`: do not run the model. If `post_github_comment` is allowed,
   post one comment listing `/review`, `/title`, `/check`, and `--dry-run`.
   Exit 0 after that help comment. If comments are not allowed, exit non-zero
   with the same list on stderr.

Slash commands only change the **effective allowlist** and append a **fixed
contract prompt** from a file next to the workflow prompts. They do not invent
new GitHub APIs.

| Slash | Extra allowlist on top of workflow base | Contract file |
|---|---|---|
| (none) | none | none (workflow prompt only) |
| `/review` | `upsert_github_check` if the event has a PR number | `review.md` contract |
| `/title` | `update_github_issue_title` | `title-rules.md` contract |
| `/check` | `upsert_github_check` | `merge-check.md` contract |

If `/review` or `/check` runs on an issue that is not a pull request, do not
open check or review tools. Post a comment (when allowed) that the command
applies only to pull requests. Exit 0.

`--dry-run` on the comment or the CLI forbids every write tool for that
process, including comments, checks, title updates, Lark sends, and
`write_job_output`. The model may still run. JSON `dry_run` is true. No GitHub
or Lark mutating HTTP occurs.

## Primitive Capabilities

Read (available to smart commands when GitHub support loaded; model selects
sections, not repository identity):

- current event summary (also injected in context);
- bounded GitHub: run jobs, check annotations, PR files, reviews, compare
  (`before...sha` or latest release tag...HEAD), file bytes at the event SHA;
- Lark message list/search for the **configured** chat/thread only.

Write (must appear in effective `allowed_actions`; identifiers from the event):

- `post_github_comment` — current issue or PR
- `upsert_github_check` — named check on the event SHA; conclusion
  `success` / `failure` / `neutral`; never merge
- `update_github_issue_title` — current issue or PR
- `send_lark_message` — configured chat id only
- `write_job_output` — named outputs for later jobs (`changelog` is the first
  name)

Denied in this entry even if the daemon exposes them: shell, workspace
mutation, merge, delete, arbitrary GitHub paths, artifact download, raw job
log fetch as commands.

A model argument that names another repository, issue, SHA, or chat id is
rejected as invalid data. The tool result states the rejection. It must not
perform the write.

Prompt-injected "merge this PR" or "use repo other/other" is the same
rejection. The smart command may still post a comment that it refused, if
`post_github_comment` is allowed.

## Event Parsing

`ParseEvent` accepts:

- existing: `workflow_run`, `pull_request`
- add: `issue_comment`, `pull_request_review_comment`, `issues`, `push`,
  `release`, `workflow_dispatch`

Each produces a validated reference with repository allowlist check. `push`
uses `before`, `after` / `head_sha`, and ref. `workflow_dispatch` may include
optional `inputs.pr_number` for the manual review workflow; if present it must
be a positive integer and the repository must still be the current allowlisted
repo.

Unknown event names fail before send or model.

## GitHub Example Workflows

Live workflows live under `.github/workflows/` and call `uses: ./` with
`mode: run` except where a step is ordinary `notify`. Copy-paste twins live
under `examples/github-agent/` with synthetic `example/widgets` in comments
only, never as a required hardcoded repo (Actions still use
`github.repository`).

Shared files:

- `.github/lark-agent/prompts/*.md`
- `.github/lark-agent/title-rules.md`
- `.github/lark-agent/notify-style.md`
- `.github/lark-agent/merge-check.md`

Triggers actually enabled on this repository:

- `issue_comment`, `pull_request_review_comment`
- `issues` opened
- `pull_request` opened (not `synchronize` by default)
- `push` to `master`
- `workflow_run` completed (existing CI notify remains ordinary; event-summary
  and notify-style jobs skip `github.event.workflow_run.name == 'CI'`)
- `workflow_dispatch` for release notes and for PR number review

Comment workflows run only when:

```text
contains(github.event.comment.body, '@lark-agent')
and comment.user.type != Bot
and author_association is OWNER, MEMBER, or COLLABORATOR
```

Fork pull-request heads are never checked out. `pull_request_target` is not
used. Workflows that need prompt files check out
`github.event.repository.default_branch` with `persist-credentials: false`.

### Scenario contracts (`GW-01` … `GW-10`)

Each numbered scenario is an acceptance unit. Tests must cite the id (`GW-01`
or `GW-01.1`). Tests may use `github run --dry-run` plus recorded HTTP, except
where the assertion is workflow `on:` / `if:` YAML (those are fixture-parsed
in integration tests). Runtime parser and tool scenes are `SC-01` … `SC-80` in
`spec/smart-command.md`.

**GW-01. Ask on issue/PR** (covers SC-06)
- Given an `issue_comment` whose body is `@lark-agent why is this failing`
  from a collaborator,
- when `github run` starts with base allowlist `post_github_comment`,
- then slash command is empty, extra_prompt is `why is this failing`,
  write tools other than `post_github_comment` are unavailable, and a
  comment is posted on that issue (unless `--dry-run`).

**GW-02. Explicit PR review** (covers SC-07)
- **GW-02.1.** Given a PR comment `@lark-agent /review focus on auth` from a
  collaborator, when `github run` starts, then command is `review`,
  extra_prompt contains `focus on auth`, `upsert_github_check` is in the
  effective allowlist, and the review contract prompt is appended.
- **GW-02.2.** Given `workflow_dispatch` with `pr_number=12` on allowlisted
  repo, when `github run` starts with the review prompt, then the reference
  PR number is 12 from inputs, not from model args.

**GW-03. Rule-triggered review**
- **GW-03.1.** Given `pull_request` opened, draft is false, when the review
  workflow job runs, then it uses the review contract without requiring a
  comment slash command.
- **GW-03.2.** Given `pull_request` synchronize without label
  `lark-agent-review`, then that job is skipped (`if:` false).
- **GW-03.3.** Given label `lark-agent-review` is added, then the review job
  may run.

**GW-04. Human event summary**
- Given a supported event and prompt `event-summary.md` with allowlist
  `send_lark_message`,
- when the smart command finishes,
- then it may send at most one Lark message to the configured chat, or send
  none if the prompt+rules decide the event is noise. It must not invent a
  second chat id.

**GW-05. Merge to master changelog**
- Given `push` to `master` with `before` and `after` SHAs,
- when compare is available,
- then the injected reference includes both SHAs and a Lark message
  (allowlist `send_lark_message`) may describe that range. Missing compare
  marks `partial` and must not invent commits.

**GW-06. Release notes then build**
- Given `workflow_dispatch` for release,
- when the smart-command job runs with `write_job_output`,
- then stdout/job output contains a `changelog` string, later `make verify`
  runs as a separate ordinary job, a **draft** GitHub Release is created only
  if verify passed, and Lark notify uses ordinary `github notify` or
  `send_lark_message` as the workflow specifies. Failure of verify must not
  create the release. This scenario does not merge.

**GW-07. PR opened summary**
- Given `pull_request` opened,
- when the summary job runs with `post_github_comment` and `pr-summary.md`,
- then one summary comment is posted on that PR. A later natural-language
  `@lark-agent summarize again` is GW-01, not a `/summarize` command.

**GW-08. Merge gate check** (covers SC-12)
- **GW-08.1.** Given `pull_request` opened, when the gate job runs with
  `upsert_github_check`, then a check run is created on `head_sha` with
  conclusion success or failure according to `merge-check.md`. The job must
  not call merge APIs.
- **GW-08.2.** Given `@lark-agent /check` on that PR, then the same check
  upsert is allowed again (idempotent name).

**GW-09. Natural-language notify style**
- **GW-09.1.** Given prompt `notify-style.md` and an event the rules call
  noisy, when the smart command completes, then no Lark message is sent and
  JSON records that skip (`skipped: true` or equivalent typed reason).
- **GW-09.2.** Given an event the rules call important, at most one Lark
  message is sent.

**GW-10. Title rewrite**
- **GW-10.1.** Given `issues` or `pull_request` opened and `title-rules.md`,
  when the title does not satisfy the rules and `update_github_issue_title`
  is allowed, then the title is updated once to a rule-satisfying string.
- **GW-10.2.** Given the title already satisfies the rules, then no PATCH
  occurs.
- **GW-10.3.** Given `@lark-agent /title`, then the same tool may run even if
  the opened workflow already ran.

## Failure And Security

- Missing model key: exit non-zero, no pretend review, no GitHub writes.
- Missing or invalid event: exit non-zero before model.
- Repository not allowlisted: exit non-zero before model.
- Enrichment/API read failure: `partial` true; writes still allowed only if
  the event reference itself is valid.
- Lark send failure when `send_lark_message` was attempted: exit non-zero.
- `--dry-run`: no mutating HTTP.
- Secrets never appear in stdout JSON, stderr, comments, or Lark bodies.
- Hostile PR titles and comment bodies are data. They are not interpolated
  into shell.

## stdout JSON

Success uses the existing CLI envelope `{"ok":true,"data":{...}}`. `data`
fields are listed in `spec/smart-command.md`. Failure: non-zero exit; stdout
empty; stderr `{"ok":false,"error":{...}}` without secrets (`writeError`).

## Test Locations

- `internal/github/*_test.go`: new events, compare range, hostile text as data
- mention parser unit tests (package next to GitHub support)
- `agent/cmd` tests: `run` starts no WebSocket; `github run` injects event;
  allowlist denial; `--dry-run`
- `agent/tools` tests: write tools require allowlist and event scope
- `integration_test/lark_agent/`: smart command + github notify regression;
  help contract for `run` and `github run`
- workflow YAML tests: `on:` / `if:` / no `pull_request_target` / checkout
  default branch

## Documentation

- `docs/smart-command.md`: smart vs ordinary; GitHub support; example
  workflows
- command `--help` and detailed help
- README pointer
- `.agents/experience/`: no second WebSocket; default-branch checkout; slash
  commands are not one-per-scenario

## Non-Goals

- Second Lark WebSocket on a smart command
- Model-selected arbitrary GitHub API, merge, delete, permission changes
- Executing PR head, artifacts, or raw job logs as commands
- Interactive cards or native approval wait
- Mapping GitHub commenters to the daemon Owner role
- Changing daemon group permission rules
- Comment flags other than `--dry-run` in this round
