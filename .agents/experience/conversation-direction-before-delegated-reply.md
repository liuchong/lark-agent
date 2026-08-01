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

For ordinary private messages, `unanswered` must be grounded in an exact quote
from the target message itself. Context can explain why the quote matters, but
must not invent a coding task from the owner's earlier question. If the target
intent is answer, acknowledgement, reaction, continuation, or reply and no
target quote exists, normalize to `no_reply_needed`.

Owner acknowledgement reactions are deterministic Go evidence, not model
claims. Read reactions on the exact target with user identity, accept only the
configured owner's allowlisted acknowledgement emoji, and fail closed when the
reaction API cannot be read.

Owner-authored non-assistant messages need a deterministic intake guard before
durable queueing. A later lexical relevance fallback must never re-admit an
outgoing owner message and make the agent reply to the owner.
