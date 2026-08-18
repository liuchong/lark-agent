package taskrules

// Template is a generic private outline. It must not encode a real workplace
// policy or copied production conversation.
const Template = `# Task rules

This private file tells the assistant which messages create work for you.
Write ordinary Markdown. It is not a programming language.

## Responsibilities

- Describe the work you want the assistant to handle on your behalf.
- Describe the work it must leave alone.

## Handle

- Messages that explicitly ask you to confirm, investigate, fix, or decide.

## Ignore

- Informational announcements, copies, and discussion opinions that do not ask
  you to act.

## How to reply or investigate

- Use the project path named in the request when one is given.
- Stop when evidence is missing instead of guessing.

## Limits

- Do not enlarge workspace access, skip approval, change send identity, or
  grant write permission.

## Examples

- Ignore: a group status announcement that only informs you.
- Handle: an explicit request that you confirm, investigate, or act.
`
