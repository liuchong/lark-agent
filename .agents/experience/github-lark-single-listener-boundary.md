# GitHub-Lark single-listener boundary

Use one Lark application identity across two different execution shapes:

- the installed daemon is the only long-running WebSocket event consumer;
- a GitHub Action is a short-lived HTTP-only message sender.

Starting a second WebSocket connection with the same application is not a
broadcast mechanism. Event delivery can be distributed between connections, so
parallel listeners would make either process miss events.

Trust does not come from visible message text. A GitHub reference becomes
model-visible only after the daemon verifies that the quoted/root message:

1. is in the same Lark chat;
2. was authored by the current Lark application;
3. carries a valid versioned marker whose HMAC verifies with the same Lark app
   secret;
4. names an allowlisted repository;
5. does not conflict with an already persisted reference.

The model may select bounded read sections, but repository, pull request, run,
API base URL, and credentials are fixed outside model arguments. Keep GitHub
mutation absent rather than relying on prompts to avoid it.

For `workflow_run`, execute only the action implementation checked out from the
trusted default branch. Never check out the triggering PR head, consume its
artifacts, or execute repository code supplied by that run.
