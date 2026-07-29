# Preserve Installer Model Environment

## Goal

Upgrading an existing macOS installation must not disable model-backed replies
merely because the shell launching the installer does not export model
variables.

## Directly Applicable Rules

- "所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按
  red -> green -> refactor 实现。"
- "每项逻辑变化必须有 `integration_test/` 覆盖；没有相应集成测试时不得宣称完成。"
- Secrets must remain outside the repository, logs, plist, and command line.
- Verification and commit must complete before production installation.

## Business Design

The installed private environment is durable per-user state. The installer
updates it through a dedicated atomic writer:

- an unset known variable preserves its installed entry;
- an explicitly supplied non-empty variable replaces its installed entry;
- an explicitly supplied empty variable removes its installed entry;
- unrelated private environment entries are preserved;
- a new installation with no supplied variables creates an empty `0600` file.

The existing installation backup and rollback continue to cover the complete
private environment file. The writer never evaluates or prints its contents.

## BDD Acceptance

### Scenario: Upgrade without exported model variables

Given an installed private environment contains model provider values,
when the installer runs with all three `OPENAI_*` variables unset,
then the file content is preserved and its mode is `0600`.

### Scenario: Partial explicit update

Given an installed private environment contains all model provider values,
when only `OPENAI_MODEL` is supplied,
then only the model changes and the key and base URL remain intact.

### Scenario: Explicit removal

Given an installed private environment contains an API key,
when `OPENAI_API_KEY` is explicitly supplied as empty,
then only that entry is removed.

### Scenario: Rollback

Given an installation updates the private environment and a later install step
fails,
when rollback completes,
then the previous private environment is restored before any old service is
reloaded.

## Test Locations

- `integration_test/lark_agent/migration_install_test.go`: atomic writer
  semantics and a complete reinstall preserving an existing environment.

## Non-Goals

- Moving model secrets into YAML, plist, command arguments, or repository files.
- Validating provider credentials during the file update.
- Changing model-provider selection semantics.

## Completion

The change is complete when the red/green integration tests pass, shell syntax
is valid, documentation and AI-facing experience agree, the fix is committed
and pushed, and a real installer run with `OPENAI_*` unset leaves the live
daemon model-configured.
