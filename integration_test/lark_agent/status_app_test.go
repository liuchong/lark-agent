package larkagent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusAppOpensStructuredPopover(t *testing.T) {
	source := statusAppSource(t)
	for _, expected := range []string{
		"NSPopover",
		"leftMouseUp",
		"rightMouseUp",
		"NSStackView",
		"Service",
		"Queue",
		"Task Rules",
		"Recent Work",
		"token_configured",
		"file_name",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("status app is missing structured popover contract %q", expected)
		}
	}
}

func TestStatusAppDoesNotUseRawJSONAsPrimaryStatus(t *testing.T) {
	source := statusAppSource(t)
	for _, forbidden := range []string{
		`show(runAgent(["daemon", "status"])`,
		`show(runAgent(["queue", "list"])`,
		`action["request_json"]`,
		"request_json",
		"response_json",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("status app still uses raw JSON as a primary status surface: %q", forbidden)
		}
	}
	if strings.Count(source, `"doctor"`) < 1 {
		t.Fatal("status popover never loads doctor for structured detail")
	}
	if !strings.Contains(source, "withTimeInterval: 10") {
		t.Fatal("status app lost the bounded 10-second icon refresh")
	}
	refreshBlock := source
	if idx := strings.Index(source, "withTimeInterval: 10"); idx >= 0 {
		end := idx + 800
		if end > len(source) {
			end = len(source)
		}
		refreshBlock = source[idx:end]
	}
	if strings.Contains(refreshBlock, `"doctor"`) {
		t.Fatal("10-second icon refresh must not invoke doctor")
	}
	cheap := functionSource(source, "collectCheapSnapshot")
	for _, forbidden := range []string{`"doctor"`, `"list"`, `"rules"`} {
		if strings.Contains(cheap, forbidden) {
			t.Fatalf("icon refresh snapshot must stay local; found %s", forbidden)
		}
	}
}

func TestStatusAppOmitsSecretsFromPanelSource(t *testing.T) {
	source := statusAppSource(t)
	for _, forbidden := range []string{
		"app_secret",
		"OPENAI_API_KEY",
		"user_access_token",
		"refresh_token",
		"github_pat_",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("status app source mentions secret material %q", forbidden)
		}
	}
}

func TestStatusAppCompilesStructuredPopover(t *testing.T) {
	if _, err := exec.LookPath("swiftc"); err != nil {
		t.Skip("swiftc is not available")
	}
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "macos", "LarkAgentStatus", "*.swift"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no status app Swift sources")
	}
	out := filepath.Join(t.TempDir(), "LarkAgentStatus")
	args := append([]string{"-framework", "AppKit", "-o", out}, files...)
	cmd := exec.Command("swiftc", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status app failed to compile: %v\n%s", err, output)
	}
}

func functionSource(source, name string) string {
	needle := "func " + name
	start := strings.Index(source, needle)
	if start < 0 {
		return ""
	}
	rest := source[start+len(needle):]
	next := strings.Index(rest, "\n    private func ")
	if next < 0 {
		next = strings.Index(rest, "\n    @objc private func ")
	}
	if next < 0 {
		return source[start:]
	}
	return source[start : start+len(needle)+next]
}

func statusAppSource(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "macos", "LarkAgentStatus", "*.swift"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no status app Swift sources")
	}
	var b strings.Builder
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}
