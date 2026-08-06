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
