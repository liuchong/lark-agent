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
		"无状态工作可自动续跑",
		"旧回复草稿必须由 Owner 显式恢复",
		"外部动作绝不重放",
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
			args: []string{"init", "--help"},
			want: []string{
				"--owner-name",
				"--preferred-language",
				"--fallback-language",
			},
		},
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
			want: []string{
				"--work-id",
				"--message-id",
				"--force-terminal",
				"delegated context and unsent reply candidates require explicit resume",
				"never sends or hydrates the old draft or context",
				"结果不确定",
			},
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
			want: []string{
				"--chat-query",
				"configured and validation groups",
				"Non-owner requests are read-only",
				"environment reconnaissance",
				"all inbound human private messages",
				"three-minute semantic owner-answer window",
			},
		},
		{
			args: []string{"github", "notify", "--help"},
			want: []string{"--chat-id", "--dry-run", "GITHUB_EVENT_PATH", "HTTP-only"},
		},
		{
			args: []string{"github", "auth", "--help"},
			want: []string{"login", "status", "Keychain", "stdin"},
		},
		{
			args: []string{"memory", "--help"},
			want: []string{"list", "add", "delete", "feedback", "confirmed"},
		},
		{
			args: []string{"memory", "delete", "--help"},
			want: []string{"ID", "--confirm", "tombstone"},
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
		"无状态只读",
		"对话调查和未发送回复候选保持中断",
		"结果不确定的外部动作绝不重放",
		"owner.preferred_language",
		"assistant.reply_scope",
		"policy.reply_scope",
		"policy.private_reply_scope",
		"policy.owner_wait",
		"lark-agent memory list",
		"all_groups",
		"all_private",
		"非 Owner 私聊机器人或直接 @机器人时保持静默",
	} {
		if !strings.Contains(string(readme), want) {
			t.Fatalf("README missing %q", want)
		}
	}
	configuration, err := os.ReadFile(filepath.Join(root, "docs/configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"policy.investigation_progress",
		"agent.vision_model",
		"agent.max_context_images",
		"max_context_image_bytes",
		"max_context_image_total_bytes",
		"reply_confidence_min: 0.70",
		"inspect_git_history",
	} {
		if !strings.Contains(string(configuration), want) {
			t.Fatalf("configuration docs missing %q", want)
		}
	}
	operations, err := os.ReadFile(filepath.Join(root, "docs/operations.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/tasks",
		"/task",
		"/memory",
		"语义判断",
		"调查主题",
		"调查状态",
		"上下文证据",
	} {
		if !strings.Contains(string(operations), want) {
			t.Fatalf("operations docs missing %q", want)
		}
	}
}
