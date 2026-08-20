# Configured Output Language For Smart Commands

`spec/behavior.md` and `spec/smart-command.md` are the long-lived contract. This
file records why this round exists.

## Problem

Smart commands produced outward content in whatever language the prompt file
happened to use. The runtime never carried a configured outward language into a
smart-command run:

- `smartcmd` built the context bundle with a synthetic user profile that had no
  language fields, so `resolvedBundleLanguage` fell through to script inference
  over the prompt text. Every shipped prompt file is English, so every
  smart-command Lark message and GitHub comment was English regardless of
  configuration.
- `owner.preferred_language` and `owner.fallback_language` existed but described
  one conversational owner. Nothing described the outward language of the
  product as a whole, and smart commands never read them.
- Language was enforced only on `reply` and `request_approval` decisions.
  Smart commands may only finish as `record`, so their outward text passed
  through the write tools with no language check at all. A prompt hint was the
  single control, and a hint is not a guarantee.
- The deterministic help comments posted for an unknown slash command and for
  `/review` or `/check` outside a pull request were English string constants.

Separately, outward smart-command content restated the diff and leaked
repository-internal vocabulary: scene ids such as `SC-81` and lower-camel-case
Go identifiers appeared in a Lark message, where no reader can resolve them.

## Decisions

1. Outward language becomes product-wide configuration: `output.language`
   (`auto`, `zh-CN`, `en-US`) and `output.fallback_language` (`zh-CN`,
   `en-US`). `owner.preferred_language` and `owner.fallback_language` remain and
   narrow to what their names claim: a per-owner override for owner-facing
   conversational work.
2. Smart commands never infer outward language from prompt text. Prompt files
   are instructions to the model, not a language sample. A smart command uses
   `output.language` when concrete, otherwise `output.fallback_language`.
3. `--output-language` and `LARK_AGENT_OUTPUT_LANGUAGE` override configuration
   for one run so GitHub Actions can select a language without a config file.
   An unsupported value fails closed before the model runs.
4. The resolved language is enforced where outward text actually leaves the
   process: the write-gate rejects a language mismatch in
   `post_github_comment`, `send_lark_message`, `upsert_github_check` prose, and
   `write_job_output`. The model sees a typed tool error and rewrites.
5. Title fields stay exempt. `update_github_issue_title` and the
   `upsert_github_check` title are repository artifacts governed by the
   English Conventional Commits rule in `AGENTS.md`, not outward prose.
6. Deterministic outward help text moves into `agent/locale` and follows the
   resolved language.
7. The smart-command system prompt states the outward content rules that a
   per-repository prompt file should not have to repeat: conclusion before
   detail, and no repository-internal scene ids or code identifiers in text
   addressed to a chat.

## Not in this round

Only `zh-CN` and `en-US` exist. Adding a third language is a separate round
because `agent/locale` renders deterministic prose per language and every
addition needs its own acceptance evidence.
