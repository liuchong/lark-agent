package larkagent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpExposesStandaloneRecoveryCommands(t *testing.T) {
	root := repositoryRoot(t)
	cmd := exec.Command("go", "run", "./cmd/lark-agent", "--help")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("help failed: %v\n%s", err, output)
	}
	text := string(output)
	for _, fragment := range []string{
		"doctor",
		"queue",
		"auth",
		"不会自动回放",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("help missing %q:\n%s", fragment, text)
		}
	}
}

func TestStandaloneDocsAndDetailedHelpStaySynchronized(t *testing.T) {
	bin := buildAgentBinary(t)
	cases := []struct {
		args []string
		want []string
	}{
		{
			args: []string{"doctor", "--help"},
			want: []string{"--lark-only", "Lark SDK", "credentials"},
		},
		{
			args: []string{"auth", "login", "--help"},
			want: []string{"JSON", "stdin", "Keychain"},
		},
		{
			args: []string{"queue", "inspect", "--help"},
			want: []string{"--work-id", "--message-id", "never replays work"},
		},
		{
			args: []string{"queue", "resume", "--help"},
			want: []string{"--work-id", "--message-id", "--force-terminal", "never replayed", "不会自动回放"},
		},
		{
			args: []string{"queue", "backfill", "--help"},
			want: []string{"--chat-query", "--since", "--until", "@Owner", "never advances the normal poll cursor"},
		},
		{
			args: []string{"daemon", "install-app", "--help"},
			want: []string{"--write", "--load", "--program", "--poll-interval"},
		},
		{
			args: []string{"daemon", "run", "--help"},
			want: []string{"--chat-query", "configured and validation groups"},
		},
	}
	for _, testCase := range cases {
		code, stdout, stderr := runAgent(t, bin, testCase.args...)
		if code != 0 {
			t.Fatalf("%v exit=%d stderr=%s", testCase.args, code, stderr)
		}
		for _, want := range testCase.want {
			if !strings.Contains(stdout, want) {
				t.Fatalf("%v help missing %q:\n%s", testCase.args, want, stdout)
			}
		}
	}

	root := repoRoot(t)
	for _, relative := range []string{
		"README.md",
		"docs/install-macos.md",
		"docs/configuration.md",
		"docs/operations.md",
		"docs/development.md",
		"spec/behavior.md",
		"spec/architecture.md",
	} {
		data, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "com.larksuite.lark-agent") {
			t.Fatalf("%s still documents old service label", relative)
		}
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"official Go SDK",
		"com.liuchong.lark-agent",
		"跨重启工作不会自动回放",
		"assistant.reply_scope",
		"policy.reply_scope",
		"all_groups",
	} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("README missing %q", want)
		}
	}
}
