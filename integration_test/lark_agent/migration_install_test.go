package larkagent_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuchong/lark-agent/agent/config"
	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/storage"
)

func TestQueueRetryCannotBypassExplicitPriorSessionResume(t *testing.T) {
	bin := buildAgentBinary(t)
	statePath := filepath.Join(t.TempDir(), "state.db")
	first, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.EnqueueEvent(domain.NormalizedEvent{
		Source:    domain.SourcePoll,
		EventID:   "evt-cli-prior-session-retry",
		MessageID: "om_cli_prior_session_retry",
	}); err != nil {
		t.Fatal(err)
	}
	item, ok, err := first.ClaimNext("worker-a")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := storage.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	command := exec.Command(
		bin,
		"--state", statePath,
		"queue", "retry",
		"--id", strconv.FormatInt(item.ID, 10),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("queue retry unexpectedly resumed prior-session work: %s", output)
	}
	if !strings.Contains(string(output), "not eligible for queue retry") {
		t.Fatalf("queue retry output=%s err=%v", output, err)
	}
	items, err := current.ListWorkItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != domain.StatusInterrupted {
		t.Fatalf("items=%+v", items)
	}
}

func TestMacOSPrivateEnvUpdaterPreservesUnsetValues(t *testing.T) {
	appSupport := filepath.Join(t.TempDir(), "Library", "Application Support", "lark-agent")
	if err := os.MkdirAll(appSupport, 0o700); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(appSupport, "env")
	original := strings.Join([]string{
		"OPENAI_API_KEY=existing-key",
		"OPENAI_BASE_URL=https://existing.example.test/v1",
		"OPENAI_MODEL=existing-model",
		"CUSTOM_PRIVATE_SETTING=preserved",
		"",
	}, "\n")
	if err := os.WriteFile(envPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	updater := filepath.Join(repoRoot(t), "scripts", "macos", "update-private-env.sh")
	run := func(extra ...string) []byte {
		t.Helper()
		command := exec.Command("bash", updater, envPath)
		command.Env = append(withoutEnvironmentKeys(
			os.Environ(),
			"OPENAI_API_KEY",
			"OPENAI_BASE_URL",
			"OPENAI_MODEL",
		), extra...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("private env updater failed: %v\n%s", err, output)
		}
		if len(output) != 0 {
			t.Fatalf("private env updater printed data: %q", output)
		}
		data, err := os.ReadFile(envPath)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(envPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("private env mode=%#o, want 0600", info.Mode().Perm())
		}
		return data
	}

	if got := run(); string(got) != original {
		t.Fatalf("unset variables changed private env:\n%s", got)
	}

	updated := string(run("OPENAI_MODEL=updated-model"))
	for _, want := range []string{
		"OPENAI_API_KEY=existing-key",
		"OPENAI_BASE_URL=https://existing.example.test/v1",
		"OPENAI_MODEL=updated-model",
		"CUSTOM_PRIVATE_SETTING=preserved",
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("partial update missing %q:\n%s", want, updated)
		}
	}
	if strings.Contains(updated, "OPENAI_MODEL=existing-model") {
		t.Fatalf("partial update retained old model:\n%s", updated)
	}

	cleared := string(run("OPENAI_API_KEY="))
	if strings.Contains(cleared, "OPENAI_API_KEY=") {
		t.Fatalf("explicit empty value did not remove API key:\n%s", cleared)
	}
	for _, want := range []string{
		"OPENAI_BASE_URL=https://existing.example.test/v1",
		"OPENAI_MODEL=updated-model",
		"CUSTOM_PRIVATE_SETTING=preserved",
	} {
		if !strings.Contains(cleared, want) {
			t.Fatalf("explicit removal lost %q:\n%s", want, cleared)
		}
	}

	newPath := filepath.Join(appSupport, "new-env")
	command := exec.Command("bash", updater, newPath)
	command.Env = withoutEnvironmentKeys(
		os.Environ(),
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_MODEL",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("new private env update failed: %v\n%s", err, output)
	} else if len(output) != 0 {
		t.Fatalf("new private env updater printed data: %q", output)
	}
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("new private env is not empty: %q", data)
	}
	info, err := os.Stat(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("new private env mode=%#o, want 0600", info.Mode().Perm())
	}
}

func TestMacOSInstallerInstallsCleanHomeWithoutLoading(t *testing.T) {
	home := t.TempDir()
	appSupport := filepath.Join(home, "Library", "Application Support", "lark-agent")
	configPath := filepath.Join(home, ".config", "lark-agent", "config.yaml")
	statePath := filepath.Join(appSupport, "state.db")
	fakeBinDir := filepath.Join(home, "fake-bin")
	if err := os.MkdirAll(fakeBinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeLaunchctlState := filepath.Join(home, "fake-launchctl-loaded")
	fakeLaunchctlPID := filepath.Join(home, "fake-launchctl-pid")
	fakeLaunchctlLog := filepath.Join(home, "fake-launchctl.log")
	fakeStoppedState := filepath.Join(home, "fake-stopped-state")
	fakeRollbackState := filepath.Join(home, "fake-rollback-state")
	fakeLaunchctl := filepath.Join(fakeBinDir, "launchctl")
	if err := os.WriteFile(fakeLaunchctl, []byte(`#!/bin/sh
state=${FAKE_LAUNCHCTL_STATE:?}
pid_file=${FAKE_LAUNCHCTL_PID:?}
log=${FAKE_LAUNCHCTL_LOG:?}
failed_bootstrap=$state.bootstrap-failed
db="$HOME/Library/Application Support/lark-agent/state.db"
snapshot() {
  target=$1
  rm -rf "$target"
  mkdir -p "$target"
  for source in "$db" "$db-wal" "$db-shm"; do
    if [ -e "$source" ]; then
      cp -p "$source" "$target/$(basename "$source")"
    fi
  done
}
case "$1" in
  print)
    case "$2" in
      *com.liuchong.lark-agent)
        [ -f "$state" ] || exit 1
        printf '%s\n' 'state = running'
        ;;
      *) exit 1 ;;
    esac
    ;;
  bootout)
    printf '%s\n' bootout >> "$log"
    snapshot "${FAKE_LAUNCHCTL_STOPPED_STATE:?}"
    if [ -f "$pid_file" ]; then
      kill "$(cat "$pid_file")" 2>/dev/null || true
      rm -f "$pid_file"
    fi
    if [ "${FAKE_LAUNCHCTL_DELAY_BOOTOUT:-0}" = 1 ]; then
      (sleep 0.3; rm -f "$state") >/dev/null 2>&1 &
    else
      rm -f "$state"
    fi
    ;;
  bootstrap)
    if [ -f "$state" ]; then
      printf '%s\n' duplicate-bootstrap >> "$log"
      exit 1
    fi
    if [ "${FAKE_LAUNCHCTL_FAIL_BOOTSTRAP:-0}" = 1 ] && [ ! -f "$failed_bootstrap" ]; then
      : > "$failed_bootstrap"
      printf '%s\n' bootstrap-failed >> "$log"
      exit 1
    fi
    printf '%s\n' bootstrap >> "$log"
    : > "$state"
    snapshot "${FAKE_LAUNCHCTL_ROLLBACK_STATE:?}"
    "$HOME/Library/Application Support/lark-agent/bin/lark-agent-daemon" \
      --config "$HOME/.config/lark-agent/config.yaml" \
      --state "$HOME/Library/Application Support/lark-agent/state.db" \
      daemon run --live --chat-query Test Group --poll-interval 1h \
      >> "$HOME/Library/Logs/lark-agent/fake-daemon.log" 2>&1 &
    printf '%s\n' "$!" > "$pid_file"
    ;;
  *)
    exit 1
    ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Lark.AppID = "cli_test"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Workspace.Root = t.TempDir()
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(repoRoot(t), "scripts", "macos", "install-lark-agent.sh")
	goEnvOutput, err := exec.Command("go", "env", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		t.Fatalf("resolve Go caches: %v", err)
	}
	goCaches := strings.Split(strings.TrimSpace(string(goEnvOutput)), "\n")
	if len(goCaches) != 2 {
		t.Fatalf("unexpected Go cache output: %q", goEnvOutput)
	}
	command := exec.Command("bash", script)
	command.Dir = repoRoot(t)
	installerEnv := append(withoutEnvironmentKeys(
		os.Environ(),
		"OPENAI_API_KEY",
		"OPENAI_BASE_URL",
		"OPENAI_MODEL",
	),
		"HOME="+home,
		"PATH="+fakeBinDir+":"+os.Getenv("PATH"),
		"FAKE_LAUNCHCTL_STATE="+fakeLaunchctlState,
		"FAKE_LAUNCHCTL_PID="+fakeLaunchctlPID,
		"FAKE_LAUNCHCTL_LOG="+fakeLaunchctlLog,
		"FAKE_LAUNCHCTL_STOPPED_STATE="+fakeStoppedState,
		"FAKE_LAUNCHCTL_ROLLBACK_STATE="+fakeRollbackState,
		"FAKE_LAUNCHCTL_DELAY_BOOTOUT=1",
		"LARK_AGENT_APP_SECRET=redacted-test-value-one",
		"LARK_AGENT_USER_ACCESS_TOKEN=redacted-test-value-two",
		"LARK_AGENT_OFFLINE_LIVE_TEST=1",
		"GOMODCACHE="+goCaches[0],
		"GOCACHE="+goCaches[1],
	)
	installLock := filepath.Join(appSupport, ".install.lock")
	if err := os.MkdirAll(installLock, 0o700); err != nil {
		t.Fatal(err)
	}
	lockedInstall := exec.Command("bash", script)
	lockedInstall.Dir = repoRoot(t)
	lockedInstall.Env = append(append([]string{}, installerEnv...),
		"INSTALL_LOAD=0",
		"OPEN_STATUS_APP=0",
	)
	lockedOutput, err := lockedInstall.CombinedOutput()
	if err == nil || !strings.Contains(string(lockedOutput), "already in progress") {
		t.Fatalf("concurrent installer was not rejected: %v\n%s", err, lockedOutput)
	}
	if err := os.Remove(installLock); err != nil {
		t.Fatal(err)
	}
	command.Env = append(append([]string{}, installerEnv...),
		"INSTALL_LOAD=0",
		"OPEN_STATUS_APP=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, output)
	}
	plist := filepath.Join(
		home,
		"Library",
		"LaunchAgents",
		"com.liuchong.lark-agent.plist",
	)
	infoPlist := filepath.Join(home, "Applications", "Lark Agent.app", "Contents", "Info.plist")
	for _, path := range []string{
		plist,
		infoPlist,
		configPath,
		statePath,
		filepath.Join(home, "Applications", "Lark Agent.app", "Contents", "MacOS", "LarkAgentStatus"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("installer output missing %s: %v\n%s", path, err, output)
		}
	}
	plistData, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plistData), "com.larksuite.") ||
		!strings.Contains(string(plistData), "com.liuchong.lark-agent") {
		t.Fatalf("unexpected LaunchAgent plist:\n%s", plistData)
	}
	for _, path := range []string{plist, infoPlist} {
		lint := exec.Command("plutil", "-lint", path)
		if output, err := lint.CombinedOutput(); err != nil {
			t.Fatalf("plutil %s: %v\n%s", path, err, output)
		}
	}

	wrapper := filepath.Join(
		appSupport,
		"bin",
		"lark-agent-daemon",
	)
	liveCtx, cancelLive := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelLive()
	wrapperData, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wrapperData), "unset LARKSUITE_CLI_CONFIG_DIR") {
		t.Fatalf("wrapper does not clear legacy CLI environment:\n%s", wrapperData)
	}
	liveCheck := exec.CommandContext(
		liveCtx,
		wrapper,
		"--config", configPath,
		"--state", statePath,
		"daemon", "run",
		"--live",
		"--chat-query", "Test Group",
		"--poll-interval", "1h",
	)
	liveCheck.Env = []string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"LARK_AGENT_APP_SECRET=redacted-test-value-one",
		"LARK_AGENT_USER_ACCESS_TOKEN=redacted-test-value-two",
		"LARK_AGENT_OFFLINE_LIVE_TEST=1",
	}
	liveOutput, err := liveCheck.CombinedOutput()
	if !errors.Is(liveCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("installed wrapper exited before lifecycle acceptance: %v\n%s", err, liveOutput)
	}
	if !strings.Contains(string(liveOutput), `"ready":true`) {
		t.Fatalf("installed wrapper did not become ready:\n%s", liveOutput)
	}
	previousStore, err := storage.OpenInspection(statePath)
	if err != nil {
		t.Fatal(err)
	}
	previousSession := previousStore.CurrentSession()
	if err := previousStore.Close(); err != nil {
		t.Fatal(err)
	}

	agentBin := filepath.Join(
		appSupport,
		"bin",
		"lark-agent",
	)
	oldAgentBin := agentBin + "-pre-upgrade"
	if err := os.Rename(agentBin, oldAgentBin); err != nil {
		t.Fatal(err)
	}
	oldMarker := "# installed-old-marker"
	oldLauncher := "#!/bin/sh\n" + oldMarker + "\nexec " + strconv.Quote(oldAgentBin) + " \"$@\"\n"
	if err := os.WriteFile(agentBin, []byte(oldLauncher), 0o700); err != nil {
		t.Fatal(err)
	}
	installedArtifacts := []string{
		filepath.Join(appSupport, "bin", "lark-agent-daemon"),
		filepath.Join(appSupport, "agent.conf"),
		filepath.Join(appSupport, "env"),
		infoPlist,
		filepath.Join(home, "Applications", "Lark Agent.app", "Contents", "MacOS", "LarkAgentStatus"),
		plist,
		configPath,
	}
	artifactSnapshot := make(map[string][]byte, len(installedArtifacts))
	for _, path := range installedArtifacts {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		artifactSnapshot[path] = data
	}
	if err := os.WriteFile(fakeLaunchctlState, []byte("loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedReinstall := exec.Command("bash", script)
	failedReinstall.Dir = repoRoot(t)
	failedReinstall.Env = append(append([]string{}, installerEnv...),
		"INSTALL_LOAD=1",
		"OPEN_STATUS_APP=0",
		"FAKE_LAUNCHCTL_FAIL_BOOTSTRAP=1",
	)
	failedOutput, err := failedReinstall.CombinedOutput()
	if err == nil || !strings.Contains(string(failedOutput), "load launch agent") {
		t.Fatalf("bootstrap failure did not fail reinstall: %v\n%s", err, failedOutput)
	}
	restoredAgent, err := os.ReadFile(agentBin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restoredAgent), oldMarker) {
		t.Fatalf("failed reinstall did not restore installed binary:\n%s\n%s", restoredAgent, failedOutput)
	}
	for path, want := range artifactSnapshot {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("failed reinstall did not restore %s\n%s", path, failedOutput)
		}
	}
	if _, err := os.Stat(fakeLaunchctlState); err != nil {
		t.Fatalf("failed reinstall did not restore loaded service: %v\n%s", err, failedOutput)
	}
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		stoppedData, stoppedErr := os.ReadFile(filepath.Join(fakeStoppedState, name))
		rollbackData, rollbackErr := os.ReadFile(filepath.Join(fakeRollbackState, name))
		if os.IsNotExist(stoppedErr) && os.IsNotExist(rollbackErr) {
			continue
		}
		if stoppedErr != nil || rollbackErr != nil || string(stoppedData) != string(rollbackData) {
			t.Fatalf(
				"failed reinstall did not restore %s before service restart: stopped_err=%v rollback_err=%v\n%s",
				name,
				stoppedErr,
				rollbackErr,
				failedOutput,
			)
		}
	}
	failedActions, err := os.ReadFile(fakeLaunchctlLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(failedActions), "bootout\nbootstrap-failed\nbootstrap\n") {
		t.Fatalf("failed reinstall did not roll back service in order:\n%s\n%s", failedActions, failedOutput)
	}
	var restoredSession domain.OnlineSession
	restoreDeadline := time.Now().Add(5 * time.Second)
	for {
		restoredStore, openErr := storage.OpenInspection(statePath)
		if openErr == nil {
			restoredSession = restoredStore.CurrentSession()
			_ = restoredStore.Close()
			if restoredSession.ID != "" &&
				restoredSession.ID != previousSession.ID &&
				restoredSession.Status == domain.OnlineSessionReady {
				break
			}
		}
		if time.Now().After(restoreDeadline) {
			t.Fatalf("restored service did not reach ready: %+v err=%v\n%s", restoredSession, openErr, failedOutput)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := os.WriteFile(fakeLaunchctlLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(appSupport, "env")
	preservedEnv := []byte(strings.Join([]string{
		"OPENAI_API_KEY=upgrade-key",
		"OPENAI_BASE_URL=https://upgrade.example.test/v1",
		"OPENAI_MODEL=upgrade-model",
		"",
	}, "\n"))
	if err := os.WriteFile(envPath, preservedEnv, 0o600); err != nil {
		t.Fatal(err)
	}
	reinstall := exec.Command("bash", script)
	reinstall.Dir = repoRoot(t)
	reinstall.Env = append(append([]string{}, installerEnv...),
		"INSTALL_LOAD=0",
		"OPEN_STATUS_APP=0",
	)
	reinstallOutput, err := reinstall.CombinedOutput()
	if err != nil {
		t.Fatalf("reinstall over loaded service failed: %v\n%s", err, reinstallOutput)
	}
	if !strings.Contains(string(reinstallOutput), `"loaded":false`) {
		t.Fatalf("reinstall did not report unloaded replacement service:\n%s", reinstallOutput)
	}
	actions, err := os.ReadFile(fakeLaunchctlLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(actions), "bootout\n") != 1 ||
		strings.Contains(string(actions), "bootstrap") ||
		strings.Contains(string(actions), "duplicate-bootstrap") {
		t.Fatalf("unexpected reinstall launchctl order:\n%s\n%s", actions, reinstallOutput)
	}
	gotEnv, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotEnv) != string(preservedEnv) {
		t.Fatalf("reinstall erased existing model environment:\n%s", gotEnv)
	}
	envInfo, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if envInfo.Mode().Perm() != 0o600 {
		t.Fatalf("reinstalled model environment mode=%#o, want 0600", envInfo.Mode().Perm())
	}
}

func withoutEnvironmentKeys(environment []string, keys ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		remove := false
		for _, key := range keys {
			if strings.HasPrefix(entry, key+"=") {
				remove = true
				break
			}
		}
		if !remove {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
