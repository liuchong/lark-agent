# Sender-derived tool authority

Model instructions are not a permission boundary. Derive invocation authority
from the durable normalized sender ID and source chat, carry it in the tool
execution context, filter the model-visible tool catalog with the same scope,
and enforce it again in the registry before calling an executor.

For Lark personal assistants, a useful fail-closed split is:

- configured owner: normal configured tools and approval rules;
- any other sender: only explicitly declared read-only tools;
- same-chat context tools: require the requested chat ID to equal the durable
  source chat;
- shell, cross-chat search, mutation, deletion, messaging, commit, and deploy:
  owner-only or side-effecting, therefore unavailable to non-owner runs.

Do not infer read-only from a missing side-effect flag. Require every tool that
is safe for non-owner use to opt in explicitly, so a newly added tool is denied
until its boundary has been reviewed.

Reply quality also needs receipt-backed work, not prompt wording alone. For a
delegated assignment or investigation, require a successful relevant read and
reject acknowledgement-only text, unsupported production claims, and
unapproved future commitments before send.
