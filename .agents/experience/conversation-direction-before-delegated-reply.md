# Conversation direction before delegated reply

An inbound private message is a semantic candidate, not proof that a reply is
needed. Before delegating a response, distinguish:

- a new question, request, invitation, or coordination need sent to the owner;
- an answer to a question initiated by the owner;
- an acknowledgement, reaction, or continuation that adds no new request;
- a conversational turn the owner already continued or handled.

The resolver needs bounded pre-target context to establish direction and
post-target context to attribute owner handling. Keep explicit group `@Owner`
work separate: it is addressed work and cannot be dismissed merely because it
is declarative.

Owner-authored non-assistant messages need a deterministic intake guard before
durable queueing. A later lexical relevance fallback must never re-admit an
outgoing owner message and make the agent reply to the owner.
