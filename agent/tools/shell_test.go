package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/liuchong/lark-agent/agent/workspace"
)

func TestShellDefinitionConfinesFileAccessToWorkspace(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Seatbelt is macOS-only")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
	root := t.TempDir()
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(ShellDefinition(scope, ShellOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := registry.Execute(context.Background(), "shell", []byte(`{
		"command":"printf workspace > result.txt && cat result.txt"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var allowedResult shellResult
	if err := json.Unmarshal([]byte(allowed.Content), &allowedResult); err != nil {
		t.Fatal(err)
	}
	if allowedResult.ExitCode != 0 || strings.TrimSpace(allowedResult.Stdout) != "workspace" || !allowedResult.Sandboxed {
		t.Fatalf("allowed=%+v", allowedResult)
	}

	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedArgs, err := json.Marshal(map[string]string{"command": "cat " + outside})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := registry.Execute(context.Background(), "shell", blockedArgs)
	if err != nil {
		t.Fatal(err)
	}
	var blockedResult shellResult
	if err := json.Unmarshal([]byte(blocked.Content), &blockedResult); err != nil {
		t.Fatal(err)
	}
	if blockedResult.ExitCode == 0 {
		t.Fatalf("outside read escaped sandbox: %+v", blockedResult)
	}
	writeArgs, err := json.Marshal(map[string]string{"command": "printf escaped > " + outside})
	if err != nil {
		t.Fatal(err)
	}
	writeBlocked, err := registry.Execute(context.Background(), "shell", writeArgs)
	if err != nil {
		t.Fatal(err)
	}
	var writeResult shellResult
	if err := json.Unmarshal([]byte(writeBlocked.Content), &writeResult); err != nil {
		t.Fatal(err)
	}
	outsideData, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if writeResult.ExitCode == 0 || string(outsideData) != "outside" {
		t.Fatalf("outside write escaped sandbox: result=%+v content=%q", writeResult, outsideData)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret, err := registry.Execute(context.Background(), "shell", []byte(`{"command":"cat .env"}`))
	if err != nil {
		t.Fatal(err)
	}
	var secretResult shellResult
	if err := json.Unmarshal([]byte(secret.Content), &secretResult); err != nil {
		t.Fatal(err)
	}
	if secretResult.ExitCode == 0 || strings.Contains(secretResult.Stdout, "TOKEN") {
		t.Fatalf("excluded secret escaped sandbox: %+v", secretResult)
	}
}

func TestShellDefinitionApprovalModeBlocksPotentialWrites(t *testing.T) {
	root := t.TempDir()
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	approvals := &fakeShellApprovals{}
	registry, err := NewRegistry(ShellDefinition(scope, ShellOptions{ApprovalRequired: true, Approvals: approvals}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkItemDedup(context.Background(), "message:om_1")
	execution, err := registry.Execute(ctx, "shell", []byte(`{"command":"touch result.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result shellResult
	if err := json.Unmarshal([]byte(execution.Content), &result); err != nil {
		t.Fatal(err)
	}
	if !result.ApprovalRequired || result.ExitCode != -1 || result.ActionID != 42 {
		t.Fatalf("result=%+v", result)
	}
}

func TestShellDefinitionPlanModeDeniesWorkspaceWrites(t *testing.T) {
	root := t.TempDir()
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(ShellDefinition(scope, ShellOptions{CodingPlanMode: true}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Execute(context.Background(), "shell", []byte(`{"command":"echo patch > agent/app/app.go"}`))
	if err == nil {
		t.Fatal("plan mode accepted a production write command")
	}
	if !strings.Contains(err.Error(), "coding plan mode") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestShellDefinitionRejectsUnboundedRecursiveSearch(t *testing.T) {
	root := t.TempDir()
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(ShellDefinition(scope, ShellOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"grep -r SampleDB .",
		"find . -name '*.go'",
		"rg SampleDB",
	} {
		_, err := registry.Execute(context.Background(), "shell", []byte(`{"command":`+strconv.Quote(command)+`}`))
		if err == nil {
			t.Fatalf("unbounded search accepted: %s", command)
		}
		if !strings.Contains(err.Error(), "use bounded code-search tools") {
			t.Fatalf("wrong error for %s: %v", command, err)
		}
	}

	if _, err := registry.Execute(context.Background(), "shell", []byte(`{"command":"rg SampleDB agent"}`)); err != nil {
		t.Fatalf("bounded rg should remain available: %v", err)
	}
}

func TestUnboundedSearchDetectionRejectsChainsAndWrappers(t *testing.T) {
	for _, command := range []string{
		"cd agent && grep -r SampleDB .",
		"env LANG=C rg SampleDB .",
		"rg -n SampleDB",
		"rg --glob '*.go' SampleDB",
		"rg SampleDB --glob '*.go'",
		"bash -c 'find . -name *.go'",
	} {
		if !isUnboundedSearchCommand(command) {
			t.Fatalf("unbounded search escaped policy: %q", command)
		}
	}
	if isUnboundedSearchCommand("rg SampleDB agent/router/router.go") {
		t.Fatal("bounded file search was rejected")
	}
}

func TestShellDefinitionRejectsDirectLarkMessageSends(t *testing.T) {
	root := t.TempDir()
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(ShellDefinition(scope, ShellOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"go run . im +messages-reply --message-id om_1 --text hi",
		"go run ./cmd/agent-api im +messages-send --chat-id oc_1 --text hi",
		"go run main.go api POST /open-apis/im/v1/messages --data '{}'",
		"go run main.go api POST /open-apis/im/v1/messages/om_1/reply --data '{}'",
		"bash -lc 'go run main.go im +messages-send --chat-id oc_1 --text hi'",
		"bash --norc -lc 'go run main.go im +messages-send --chat-id oc_1 --text hi'",
		"grep foo README.md & go run main.go im +messages-reply --message-id om_1 --text hi",
	} {
		_, err := registry.Execute(context.Background(), "shell", []byte(`{"command":`+strconv.Quote(command)+`}`))
		if err == nil {
			t.Fatalf("direct IM send command was accepted: %s", command)
		}
		if !strings.Contains(err.Error(), "direct Lark IM sends are not allowed") {
			t.Fatalf("direct IM send command failed for the wrong reason: command=%s err=%v", command, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("go run main.go im +messages-send"), 0o600); err != nil {
		t.Fatal(err)
	}
	execution, err := registry.Execute(context.Background(), "shell", []byte(`{"command":"grep -n \"go run main.go im +messages-send\" README.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result shellResult
	if err := json.Unmarshal([]byte(execution.Content), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "messages-send") {
		t.Fatalf("read-only inspection was blocked: %+v", result)
	}
}

func TestAttachCapturedShellStreamSpillsOversizedOutput(t *testing.T) {
	root := t.TempDir()
	scope, err := workspace.NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	temp, err := os.CreateTemp(root, "capture-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	full := strings.Repeat("a", 200)
	if _, err := temp.WriteString(full); err != nil {
		t.Fatal(err)
	}
	preview := limitedBuffer{max: 16}
	_, _ = preview.Write([]byte(full))
	var text, pathOut, digest string
	var size int
	var truncated bool
	if err := attachCapturedShellStream(scope, "work-1", "stdout", temp, preview, &text, &pathOut, &digest, &size, &truncated); err != nil {
		t.Fatal(err)
	}
	if !truncated || pathOut == "" || digest == "" || size != 200 || !strings.Contains(text, "output truncated") {
		t.Fatalf("text=%q path=%s digest=%s size=%d truncated=%v", text, pathOut, digest, size, truncated)
	}
	spilled, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pathOut)))
	if err != nil {
		t.Fatal(err)
	}
	if string(spilled) != full {
		t.Fatalf("spilled=%q", spilled)
	}
}

type fakeShellApprovals struct {
	approved bool
}

func (f *fakeShellApprovals) RequestShellApproval(context.Context, string, string, string) (int64, error) {
	return 42, nil
}

func (f *fakeShellApprovals) ConsumeShellApproval(context.Context, string, string, string) (int64, bool, error) {
	return 42, f.approved, nil
}

func (f *fakeShellApprovals) BeginShellAction(context.Context, string, string, string) (int64, string, bool, error) {
	return 42, "", false, nil
}

func (f *fakeShellApprovals) CompleteShellApproval(context.Context, int64, string, string) error {
	return nil
}
