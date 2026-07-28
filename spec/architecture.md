# Architecture

## Product boundary

`lark-agent` is a personal AI assistant built on the official public Go SDK for
Lark/Feishu. It owns routing, model execution, tools, scheduling, state, audit,
recovery, installation, and user-visible behavior. It does not execute `lark-cli`
as a subprocess and does not copy the official CLI's internal Go implementation.

## Dependency direction

1. `cmd/lark-agent` composes the application.
2. Agent business packages depend on Agent-owned interfaces and domain types.
3. `internal/lark` is the only production Lark transport.
4. `internal/lark` constructs `github.com/larksuite/oapi-sdk-go/v3` HTTP and
   WebSocket clients, reads credentials from Keychain references, and projects
   SDK responses/events into typed values.
5. SQLite durably records intake before work can be claimed and records external
   actions before they are attempted.
6. Workspace tools are independent of Lark transport and enforce the configured
   filesystem boundary in code.
7. `internal/github` is the only GitHub HTTP and event-decoding boundary. It
   returns Agent-owned typed values and never executes pull-request code,
   artifacts, logs, or event-derived shell commands.
8. A GitHub Action is a short-lived Lark HTTP sender. The installed daemon is
   the single WebSocket event consumer even when both processes authenticate as
   the same Lark application.

No production package imports `github.com/larksuite/cli`; no production path
depends on a local `lark-cli` executable, profile, stdout envelope, or event
stdin lifecycle. YAML stores only non-secret app and Keychain references.

GitHub follow-up tools take their repository and run identity from the verified
invocation reference, never from model arguments. Missing or untrusted
references make the tool unavailable. Non-owner invocations retain the same
read-only authority they have for workspace and same-chat evidence.

## Durable state

Every daemon process owns a unique `starting -> ready -> stopped` online
session. Intake receipts are append-only observations; one message ID links to
at most one work item, while later real-time or poll observations append
duplicate receipts. Work items carry the owning session and a unique claim
token. Model runs, tool steps, reply/notification actions, lifecycle actions,
and interruption snapshots are persisted before dependent side effects.

Verified external references are separate durable control records keyed by
provider and Lark message ID. They store canonical reference data and sender
identity, not GitHub credentials or an unbounded GitHub snapshot. The model
receives a reference only after same-chat relation and current-app verification.

Opening the database for an operator command does not create or stop a daemon
session. On startup, unfinished work from older sessions becomes `interrupted`
and cannot be claimed. `queue resume` is the only cross-session admission path.

## Process lifecycle

A daemon process creates an online session in `starting`, completes SDK,
Keychain, model, storage, intake, and scheduler preflight, then moves it to `ready`.
Only then are workers allowed to claim new work. Graceful stop pauses unfinished
current-session work, sends the durable offline notice, and records `stopped`.
Work belonging to an older session is interrupted and remains paused until the
owner explicitly resumes the exact item.

## Installation

The macOS installer uses LaunchAgent label `com.liuchong.lark-agent`, stores
logs and state under standalone lark-agent directories, and unloads a newly
loaded service if it does not create a distinct ready session. It installs only
from the current independent configuration and never reads or migrates old
command-line tool directories.

## Compatibility

The supported integration contract is the official public Go SDK and documented
Lark OpenAPI behavior. Official CLI internal packages, hidden commands, config
file formats, stdout envelopes, and process lifecycle are not compatibility
surfaces.
