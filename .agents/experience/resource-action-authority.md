# Resource action authority

- A notification or model-proposed coordinate is only a locator. Before an
  external write, load the latest typed evidence for the exact resource.
- General evidence search may rank owner-related matches first. Authorization
  queries for an exact Base record must instead rank the newest snapshot first,
  so an old assignment cannot override a later unassignment.
- Resource work intake must persist `resource_evidence_id` in every admission
  path. A synthetic work message without that durable link cannot authorize a
  later action.
- A comment reply is authorized only when the linked evidence says the owner
  was mentioned and its `file_token` and `comment_id` exactly equal the
  proposed target.
- Compare-before-write and read-back verification must compare the same
  semantic value shape used by evidence projection. Lark single-select values
  may appear as either strings or objects containing `name`, `text`, or
  `value`.
- A conversational status-update handoff is `resource_handoff` work, not a
  delegated `investigation` or `coding` progress lifecycle. Do not create or
  complete investigation progress records for it.
- Until the referenced resource is read, normalize its task summary to the
  resource-neutral operation (locate, verify fix evidence, update status).
  Nearby chat subjects are not authoritative issue identity.
