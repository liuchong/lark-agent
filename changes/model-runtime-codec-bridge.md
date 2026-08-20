# Model Runtime Codec Bridge

## Decision

Production model calls now use the `model.OpenAIChatCodec` for request and
response encoding while keeping the existing Eino-facing adapter at the agent
loop boundary. This removes the duplicate Chat Completions JSON path without
forcing the whole loop to switch to `model.Request`/`model.Turn` in one change.

## Migration Plan

1. Keep the Eino adapter as the temporary boundary and route all OpenAI Chat
   request/response serialization through the codec.
2. Move provider failure metadata and run-state reminders into the model-runtime
   request/turn structs.
3. Once the loop consumes `model.Turn` directly, enable non-chat protocols from
   the same runtime boundary and remove the protocol guard.

## Non-goals

- This does not enable `openai_responses` or `anthropic_messages` in production
  yet.
- This does not change prompt policy or tool authority.
