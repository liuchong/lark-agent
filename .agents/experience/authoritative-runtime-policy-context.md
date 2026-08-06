# Authoritative runtime policy context

When the model may answer questions about the assistant's current behavior,
validated runtime configuration must be supplied as a dedicated non-secret
policy snapshot. Workspace rules are business-project evidence and must not be
used to infer daemon mode, scopes, waits, thresholds, or retry behavior.

Keep similarly named values behavior-specific in both field names and prompt
text. In particular, distinguish a threshold used to classify whether the
owner already answered from the threshold used to send a low-risk delegated
draft automatically.

The regression contract should cover the full path:

1. map validated configuration into the snapshot;
2. preserve the snapshot through conversation building and context compaction;
3. include exact values and meanings in the model request;
4. prove an ordinary question containing command-like words still reaches the
   answer path;
5. verify the behavior with the installed bot, not only a prompt unit test.
