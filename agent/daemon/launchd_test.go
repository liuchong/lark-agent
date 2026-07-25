package daemon

import (
	"strings"
	"testing"
)

func TestLaunchdPlistIncludesProgramAndConfig(t *testing.T) {
	plist := LaunchdPlist(LaunchdConfig{
		Label:        "com.liuchong.lark-agent",
		Program:      "/usr/local/bin/lark-agent",
		ConfigPath:   "/tmp/config.yaml",
		StatePath:    "/tmp/state.db",
		Live:         true,
		ChatQuery:    "Example Group",
		PollInterval: "10s",
		StdoutPath:   "/tmp/out.log",
		StderrPath:   "/tmp/err.log",
		Environment:  map[string]string{"LARK_AGENT_TEST_ENV": "ok"},
	})
	for _, want := range []string{
		"com.liuchong.lark-agent",
		"/usr/local/bin/lark-agent",
		"--config",
		"/tmp/config.yaml",
		"--state",
		"/tmp/state.db",
		"daemon",
		"run",
		"--live",
		"--chat-query",
		"Example Group",
		"--poll-interval",
		"10s",
		"StandardOutPath",
		"/tmp/out.log",
		"StandardErrorPath",
		"/tmp/err.log",
		"EnvironmentVariables",
		"LARK_AGENT_TEST_ENV",
		"ok",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
}

func TestLaunchdProgramArguments(t *testing.T) {
	args := LaunchdProgramArguments(LaunchdConfig{
		Program:      "/bin/lark-agent",
		ConfigPath:   "/tmp/config.yaml",
		StatePath:    "/tmp/state.db",
		Live:         true,
		ChatQuery:    "Example Group",
		PollInterval: "10s",
	})
	want := []string{"/bin/lark-agent", "--config", "/tmp/config.yaml", "--state", "/tmp/state.db", "daemon", "run", "--live", "--chat-query", "Example Group", "--poll-interval", "10s"}
	if strings.Join(args, "\n") != strings.Join(want, "\n") {
		t.Fatalf("args=%q want=%q", args, want)
	}
}
