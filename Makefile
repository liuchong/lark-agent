.PHONY: build test unit-test integration-test harness-eval install-test lint license-check verify

build:
	go build ./cmd/lark-agent

unit-test:
	go test -race ./agent/... ./internal/...

integration-test:
	go test -race ./integration_test/lark_agent/...

harness-eval:
	go test -race -v ./integration_test/lark_agent -run 'TestHarnessEval'

test:
	go test -race ./...

install-test:
	bash -n scripts/macos/install-lark-agent.sh
	set -e; tmp="$$(mktemp "$${TMPDIR:-/tmp}/lark-agent-status.XXXXXX")"; trap 'rm -f "$$tmp"' EXIT; swiftc macos/LarkAgentStatus/*.swift -framework AppKit -o "$$tmp"
	go test -race ./integration_test/lark_agent -run 'TestMacOSInstallerInstallsCleanHomeWithoutLoading'

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6 run

license-check:
	# go-licenses does not classify this repository's 1PL or mathutil's BSD-3-Clause text.
	go run github.com/google/go-licenses@v1.6.0 check --ignore github.com/liuchong/lark-agent --ignore modernc.org/mathutil ./...

verify:
	test -z "$$(gofmt -l .)"
	go test -race ./...
	go vet ./...
	$(MAKE) lint
	$(MAKE) install-test
	go mod tidy
	git diff --exit-code -- go.mod go.sum
	! rg -n 'github\.com/larksuite/cli' --glob '*.go' go.mod go.sum agent cmd internal integration_test
	! go list -m all | rg 'github\.com/larksuite/cli'
	$(MAKE) license-check
