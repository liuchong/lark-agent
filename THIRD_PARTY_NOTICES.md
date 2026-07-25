# Third-party notices

## Official Lark/Feishu Go SDK

This project calls the official
[`larksuite/oapi-sdk-go`](https://github.com/larksuite/oapi-sdk-go) module as
its Lark API boundary. The SDK is distributed under the MIT License. Its license
text is preserved in [`LICENSES/MIT-Larksuite.txt`](LICENSES/MIT-Larksuite.txt).

No package under `github.com/larksuite/cli/internal`, `github.com/larksuite/cli/events`,
or `github.com/larksuite/cli/cmd/event` is copied into or linked by this module.

## Go module dependencies

Go dependencies and their exact versions are declared by `go.mod` and `go.sum`.
Their own license terms continue to apply. The repository's dependency-license
check is the authoritative generated inventory for a release candidate.

`go-licenses` does not classify the repository's custom 1PL text or the
BSD-3-Clause text shipped in `modernc.org/mathutil`'s `LICENSE`. The automated
check therefore excludes those two known entries; maintainers must verify the
root `LICENSE` and the exact selected `modernc.org/mathutil` module license
manually whenever dependencies change.
