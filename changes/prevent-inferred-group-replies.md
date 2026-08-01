# Prevent inferred group replies

## Problem

A non-owner group message without a native Owner mention could be classified as
background `record` work because it contained a relevance word such as `任务`.
The model was still invoked for that record and could promote the terminal
decision to `reply`, causing an unsolicited group response.

## Confirmed behavior

- A non-owner group message can receive a sender-facing response only when its
  original Lark event natively mentions the configured Owner and current reply
  scope allows the group.
- Inferred background work without that mention may finish only as `ignore`,
  `record`, or `notify`.
- Prompt instructions explain the allowed outcomes, but they are not the
  permission boundary.
- Immediately before every reply or approval, the Go runtime re-runs the current
  deterministic router against the original event. A disallowed model result is
  replaced by the router's non-sender-facing result.
- Held candidates are cancelled when current routing is no longer sender-facing.
  Ready approvals are lease-fenced into `blocked` before the work item is
  completed, so they cannot be sent after another resume.

## BDD acceptance

Given a non-owner group message contains `任务`, mentions another person, and
does not natively mention the configured Owner, when routing records it as
inferred background work and the model submits a reply, then the reply handler
is not called and the work completes as `record`.

Given a held candidate or ready approval was created for a message that current
routing no longer permits to reply to, when the work is reclaimed, then no
external send occurs, the candidate is cancelled or the approval is blocked,
and stale leases cannot perform the transition.

## Test and documentation impact

- Integration coverage uses the incident shape and a real SQLite queue.
- App tests cover held candidates and persisted approvals.
- Storage tests prove the approval-block transition requires the current lease.
- Operations documentation states the native mention boundary and blocked
  approval behavior. No configuration, schema migration, command, help text, or
  installation default changes are required.

## Non-goals

This change does not remove inferred background recording, alter private-message
delegation, or change replies to Owner-authored assistant requests.
