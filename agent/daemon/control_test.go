package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	vfs "github.com/liuchong/lark-agent/internal/fsx"
)

type fakeRunner struct {
	calls []string
	out   string
}

type fakeReadyChecker struct {
	err error
}

func (f fakeReadyChecker) Wait(context.Context, string, string) error {
	return f.err
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return []byte(r.out), nil
}

func TestUserPaths(t *testing.T) {
	paths := UserPaths("/Users/me")
	if paths.PlistPath != "/Users/me/Library/LaunchAgents/com.liuchong.lark-agent.plist" {
		t.Fatalf("paths=%+v", paths)
	}
	if paths.AppPath != "/Users/me/Applications/Lark Agent.app" {
		t.Fatalf("paths=%+v", paths)
	}
	if paths.BinDir != "/Users/me/Library/Application Support/lark-agent/bin" {
		t.Fatalf("paths=%+v", paths)
	}
	if paths.LogDir != "/Users/me/Library/Logs/lark-agent" {
		t.Fatalf("paths=%+v", paths)
	}
}

func TestControllerInstallWritesPlistAndLoads(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	controller := Controller{
		HomeDir: root, Runner: runner, UserDomain: "gui/501",
		ReadyChecker: fakeReadyChecker{},
	}
	status, err := controller.Install(context.Background(), InstallRequest{
		Program:      "/tmp/lark-agent",
		ConfigPath:   "/tmp/config.yaml",
		StatePath:    "/tmp/state.db",
		Live:         true,
		ChatQuery:    "Example Group",
		PollInterval: "10s",
		Load:         true,
		Environment:  map[string]string{"LARK_AGENT_TEST_ENV": "ok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.PlistPath == "" {
		t.Fatalf("status=%+v", status)
	}
	data, err := vfs.ReadFile(filepath.Join(root, "Library/LaunchAgents/com.liuchong.lark-agent.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--state") ||
		!strings.Contains(string(data), "Example Group") ||
		!strings.Contains(string(data), "LARK_AGENT_TEST_ENV") {
		t.Fatalf("plist=%s", data)
	}
	if len(runner.calls) < 1 || !strings.Contains(runner.calls[0], "bootstrap gui/501") {
		t.Fatalf("calls=%+v", runner.calls)
	}
}

func TestLaunchctlCommandEnvDropsSecretsAndCLIConfig(t *testing.T) {
	t.Setenv("HOME", "/Users/me")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", "/tmp/old-cli")
	t.Setenv("FAKE_LAUNCHCTL_STATE", "/tmp/state")
	env := strings.Join(launchctlCommandEnv(), "\n")
	if strings.Contains(env, "OPENAI_API_KEY") ||
		strings.Contains(env, "LARKSUITE_CLI_CONFIG_DIR") {
		t.Fatalf("unsafe env leaked:\n%s", env)
	}
	if !strings.Contains(env, "HOME=/Users/me") || !strings.Contains(env, "PATH=/usr/bin:/bin") {
		t.Fatalf("required env missing:\n%s", env)
	}
	if !strings.Contains(env, "FAKE_LAUNCHCTL_STATE=/tmp/state") {
		t.Fatalf("fake launchctl env missing:\n%s", env)
	}
}

func TestControllerInstallUnloadsServiceThatNeverBecomesReady(t *testing.T) {
	runner := &fakeRunner{}
	controller := Controller{
		HomeDir: t.TempDir(), Runner: runner, UserDomain: "gui/501",
		ReadyChecker: fakeReadyChecker{err: errors.New("not ready")},
	}
	_, err := controller.Install(context.Background(), InstallRequest{
		Program:    "/tmp/lark-agent",
		ConfigPath: "/tmp/config.yaml",
		StatePath:  "/tmp/state.db",
		Load:       true,
	})
	if err == nil {
		t.Fatal("installer accepted loaded but unready service")
	}
	if len(runner.calls) < 2 ||
		!strings.Contains(strings.Join(runner.calls, "\n"), "bootout gui/501/com.liuchong.lark-agent") {
		t.Fatalf("calls=%+v", runner.calls)
	}
}

func TestControllerStatusParsesRunningPID(t *testing.T) {
	runner := &fakeRunner{out: "state = running\npid = 12345\n"}
	controller := Controller{HomeDir: t.TempDir(), Runner: runner, UserDomain: "gui/501"}
	status, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Loaded || !status.Running || status.PID != 12345 {
		t.Fatalf("status=%+v", status)
	}
}

func TestControllerStopCallsBootout(t *testing.T) {
	runner := &fakeRunner{}
	controller := Controller{HomeDir: t.TempDir(), Runner: runner, UserDomain: "gui/501"}
	if _, err := controller.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) < 1 || !strings.Contains(runner.calls[0], "bootout gui/501/com.liuchong.lark-agent") {
		t.Fatalf("calls=%+v", runner.calls)
	}
}
