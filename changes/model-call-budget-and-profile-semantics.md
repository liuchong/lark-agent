# Model Call Budget And Whole-Profile Requests

`spec/behavior.md` and `spec/smart-command.md` are the long-lived contract. This
file records why this round exists.

## Problem

A `Lark Agent master changelog` run failed with one retryable transport error
and lost the whole job. Two defects behind it, both of them spec text that was
never implemented:

- `spec/behavior.md` already required retryable provider failures to be retried
  at the model step with exponential backoff and `Retry-After` handling.
  Nothing implemented it. `agent/runtime/model/failure.go` classified HTTP
  failures and `openai_model.go` even carried a `retryAfterError` type, but no
  caller ever read either one: the first failure ended the call. The
  classification also had no case for a failure that happens before any
  response arrives, which is exactly what a dropped connection or an elapsed
  timeout is.
- The per-attempt timeout defaulted to 60 seconds in three separate places. A
  reasoning model summarizing a whole push routinely needs longer, so the
  heaviest prompt in the repository was the one most likely to time out, and
  the operator had no way to raise it in Actions.

The same investigation found that a model profile only partly reached the wire.
`agent/runtime/model` holds the provider layer: codecs for `openai_chat`,
`openai_responses`, and `anthropic_messages`, and a `kimiThinking` encoder for
declared reasoning behavior. Only an integration test referenced it. Production
built its request body by hand in `OpenAICompatibleModel` and sent nothing from
`reasoning`, `capabilities`, or `stream`. A profile could declare a reasoning
effort and no request ever carried it, in the daemon or in a smart command.

Separately, `lark-agent-event-summary.yml` and `lark-agent-notify-style.yml`
declared identical triggers, so one issue, pull request, or CI completion
produced two model-written Lark messages on top of the deterministic notifier's
own message.

## Decisions

- One model call has a bounded budget that belongs to the profile: `timeout`
  bounds one attempt, `max_attempts` bounds the attempts, and both default to
  values sized for the shipped reasoning profile (120 seconds, 3 attempts). An
  unconfigured install survives a single provider blip.
- A retry decision is made from the runtime classification, not from the call
  site. Transport failures, timeouts, 429, 5xx, 529, and an empty provider
  answer are retried; 400, 401, 403, 404, and quota exhaustion return after one
  round trip. `Retry-After` wins over the local backoff, and the caller's
  cancellation or deadline ends the wait immediately without another request.
- The per-attempt timeout is applied through the request context, so it holds
  whether or not the caller injected an HTTP client. It previously lived only on
  a client the runtime constructed itself.
- Every path that calls a model sends a request built from the whole profile.
  `ModelProfileConfig.RuntimeProfile` is the single mapping, and the exported
  `ThinkingPayload` is the single reasoning encoder. A smart command in CI and
  the resident daemon differ in what triggers them and where they write, never
  in which provider traits reach the wire.
- An Actions run has no config file, so `model_reasoning_effort` and
  `model_timeout` inputs let a workflow tune the primary profile it cannot
  edit. Their product must fit the 8-minute smart-command loop budget.
- Lark-sending smart-command workflows no longer share a trigger. New issues and
  pull requests belong to the event-summary workflow; a CI completion is
  reported once by the deterministic notifier, and only a failed run earns an
  additional model-written explanation.

## Non-goals

- Routing the agent loop through the `Turn` codec interface. The provider layer
  still owns encode and decode for three protocols while production speaks
  `openai_chat` through the Eino bridge; collapsing the two request builders is
  a separate change with its own spec work.
- Streaming. `stream: required` remains a declaration the V1 runtime cannot
  honor, and `Stream` still refuses.
- Retrying at the work level for a failure the model step already exhausted.
  The existing transient retry state keeps that job.
