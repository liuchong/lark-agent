# LaunchAgent wrapper environment cleanup

When removing an old tool boundary from a macOS LaunchAgent, do not rely only on
the install command's `launchctl` environment filtering. The generated wrapper
must also explicitly `unset` retired environment keys before it sources the
agent's private env file and `exec`s the real binary.

Verification should check all three places without printing secret values:

- generated plist does not contain retired keys;
- `launchctl getenv <key>` is empty for the user launchd domain;
- the running process environment does not contain retired keys.
