# Preserve private model environment on upgrade

Treat the installed model environment as durable user state, not as a projection
of whichever variables happen to be exported by the shell running an upgrade.

For each supported key, distinguish three states:

- unset: preserve the installed entry;
- set non-empty: replace that entry;
- set empty: remove that entry.

Perform the update without sourcing or printing the old file, preserve unrelated
entries, write through a private temporary file, atomically replace the target,
and force mode `0600`. Keep the file inside the installer's existing backup and
rollback set. A production upgrade test should run with all model variables
unset and verify that the restarted process still reports model configuration.
