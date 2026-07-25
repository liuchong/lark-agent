package daemon

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/agent/storage"
	"github.com/liuchong/lark-agent/internal/apperr"
	vfs "github.com/liuchong/lark-agent/internal/fsx"
)

const Label = "com.liuchong.lark-agent"

// Paths contains user-level install locations.
type Paths struct {
	AppPath   string `json:"app_path" yaml:"app_path"`
	PlistPath string `json:"plist_path" yaml:"plist_path"`
	BinDir    string `json:"bin_dir" yaml:"bin_dir"`
	LogDir    string `json:"log_dir" yaml:"log_dir"`
	Stdout    string `json:"stdout" yaml:"stdout"`
	Stderr    string `json:"stderr" yaml:"stderr"`
}

// UserPaths returns the user-level install locations for home.
func UserPaths(home string) Paths {
	logDir := filepath.Join(home, "Library", "Logs", "lark-agent")
	return Paths{
		AppPath:   filepath.Join(home, "Applications", "Lark Agent.app"),
		PlistPath: filepath.Join(home, "Library", "LaunchAgents", Label+".plist"),
		BinDir:    filepath.Join(home, "Library", "Application Support", "lark-agent", "bin"),
		LogDir:    logDir,
		Stdout:    filepath.Join(logDir, "daemon.out.log"),
		Stderr:    filepath.Join(logDir, "daemon.err.log"),
	}
}

// CommandRunner executes launchctl. Tests provide a fake.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = launchctlCommandEnv()
	return cmd.CombinedOutput()
}

func launchctlCommandEnv() []string {
	keys := []string{"HOME", "USER", "LOGNAME", "TMPDIR", "PATH"}
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			env = append(env, key+"="+value)
		}
	}
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "FAKE_LAUNCHCTL_") {
			env = append(env, item)
		}
	}
	if !hasEnvKey(env, "PATH") {
		env = append(env, "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
	}
	return env
}

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

// Controller controls the user-level launchd service.
type Controller struct {
	HomeDir      string
	Runner       CommandRunner
	UserDomain   string
	ReadyChecker ReadyChecker
}

// ReadyChecker waits for a newly loaded daemon session to reach ready.
type ReadyChecker interface {
	Wait(context.Context, string, string) error
}

// InstallRequest describes a user-level service installation.
type InstallRequest struct {
	Program      string
	ConfigPath   string
	StatePath    string
	Live         bool
	ChatQuery    string
	PollInterval string
	Load         bool
	Environment  map[string]string
}

// Status describes installation and runtime state.
type Status struct {
	Supported bool   `json:"supported" yaml:"supported"`
	Label     string `json:"label" yaml:"label"`
	Installed bool   `json:"installed" yaml:"installed"`
	Loaded    bool   `json:"loaded" yaml:"loaded"`
	Running   bool   `json:"running" yaml:"running"`
	PID       int    `json:"pid,omitempty" yaml:"pid,omitempty"`
	PlistPath string `json:"plist_path" yaml:"plist_path"`
	AppPath   string `json:"app_path" yaml:"app_path"`
	BinDir    string `json:"bin_dir" yaml:"bin_dir"`
	LogDir    string `json:"log_dir" yaml:"log_dir"`
	LastError string `json:"last_error,omitempty" yaml:"last_error,omitempty"`
}

// NewController creates a controller using the current user's home and launchctl.
func NewController() (*Controller, error) {
	home, err := vfs.UserHomeDir()
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeFileIO, "resolve user home directory").WithCause(err)
	}
	return &Controller{HomeDir: home, Runner: execRunner{}}, nil
}

// Install writes the LaunchAgent plist and optionally bootstraps it.
func (c Controller) Install(ctx context.Context, req InstallRequest) (Status, error) {
	paths := c.paths()
	for _, dir := range []string{filepath.Dir(paths.PlistPath), paths.LogDir} {
		if err := vfs.MkdirAll(dir, 0o700); err != nil {
			return Status{}, errs.NewInternalError(errs.SubtypeFileIO, "create daemon directory: %s", dir).WithCause(err)
		}
	}
	plist := LaunchdPlist(LaunchdConfig{
		Label:        Label,
		Program:      req.Program,
		ConfigPath:   req.ConfigPath,
		StatePath:    req.StatePath,
		Live:         req.Live,
		ChatQuery:    req.ChatQuery,
		PollInterval: req.PollInterval,
		StdoutPath:   paths.Stdout,
		StderrPath:   paths.Stderr,
		Environment:  req.Environment,
	})
	if err := vfs.WriteFile(paths.PlistPath, []byte(plist), 0o600); err != nil {
		return Status{}, errs.NewInternalError(errs.SubtypeFileIO, "write launch agent plist: %s", paths.PlistPath).WithCause(err)
	}
	if req.Load {
		previousSessionID := activeSessionID(req.StatePath)
		domain, err := c.domain()
		if err != nil {
			return Status{}, err
		}
		if _, err := c.runner().Run(ctx, "launchctl", "bootstrap", domain, paths.PlistPath); err != nil {
			return Status{}, errs.NewInternalError(errs.SubtypeUnknown, "load launch agent").WithCause(err)
		}
		if err := c.readyChecker().Wait(ctx, req.StatePath, previousSessionID); err != nil {
			_, _ = c.runner().Run(ctx, "launchctl", "bootout", domain+"/"+Label)
			return Status{}, errs.NewInternalError(
				errs.SubtypeFailedPrecondition,
				"loaded daemon did not become ready",
			).WithCause(err)
		}
	}
	status, err := c.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Installed = true
	return status, nil
}

// Status reads the current LaunchAgent status.
func (c Controller) Status(ctx context.Context) (Status, error) {
	paths := c.paths()
	status := Status{
		Supported: true,
		Label:     Label,
		PlistPath: paths.PlistPath,
		AppPath:   paths.AppPath,
		BinDir:    paths.BinDir,
		LogDir:    paths.LogDir,
	}
	if _, err := vfs.Stat(paths.PlistPath); err == nil {
		status.Installed = true
	}
	domain, err := c.domain()
	if err != nil {
		return Status{}, err
	}
	out, printErr := c.runner().Run(ctx, "launchctl", "print", domain+"/"+Label)
	if printErr != nil {
		status.LastError = strings.TrimSpace(string(out))
	} else {
		status.Loaded = true
		status.PID = parseLaunchdPID(string(out))
		status.Running = status.PID > 0 || strings.Contains(string(out), "state = running")
	}
	return status, nil
}

// Start bootstraps the installed LaunchAgent.
func (c Controller) Start(ctx context.Context) (Status, error) {
	paths := c.paths()
	if _, err := vfs.Stat(paths.PlistPath); err != nil {
		return Status{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "launch agent is not installed").
			WithHint("run `lark-agent daemon install-app --write --load` first").
			WithCause(err)
	}
	domain, err := c.domain()
	if err != nil {
		return Status{}, err
	}
	if _, err := c.runner().Run(ctx, "launchctl", "bootstrap", domain, paths.PlistPath); err != nil {
		return Status{}, errs.NewInternalError(errs.SubtypeUnknown, "start launch agent").WithCause(err)
	}
	return c.Status(ctx)
}

// Stop unloads the user LaunchAgent.
func (c Controller) Stop(ctx context.Context) (Status, error) {
	domain, err := c.domain()
	if err != nil {
		return Status{}, err
	}
	if _, err := c.runner().Run(ctx, "launchctl", "bootout", domain+"/"+Label); err != nil {
		return Status{}, errs.NewInternalError(errs.SubtypeUnknown, "stop launch agent").WithCause(err)
	}
	return c.Status(ctx)
}

// Uninstall stops and removes the LaunchAgent plist. It keeps config/state.
func (c Controller) Uninstall(ctx context.Context) (Status, error) {
	if domain, err := c.domain(); err == nil {
		_, _ = c.runner().Run(ctx, "launchctl", "bootout", domain+"/"+Label)
	}
	paths := c.paths()
	if err := vfs.Remove(paths.PlistPath); err != nil {
		return Status{}, errs.NewInternalError(errs.SubtypeFileIO, "remove launch agent plist: %s", paths.PlistPath).WithCause(err)
	}
	return c.Status(ctx)
}

func (c Controller) paths() Paths {
	return UserPaths(c.HomeDir)
}

func (c Controller) runner() CommandRunner {
	if c.Runner != nil {
		return c.Runner
	}
	return execRunner{}
}

func (c Controller) readyChecker() ReadyChecker {
	if c.ReadyChecker != nil {
		return c.ReadyChecker
	}
	return stateReadyChecker{timeout: 45 * time.Second}
}

type stateReadyChecker struct {
	timeout time.Duration
}

func (c stateReadyChecker) Wait(
	ctx context.Context,
	statePath string,
	previousSessionID string,
) error {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		store, err := storage.OpenInspection(statePath)
		if err == nil {
			session := store.CurrentSession()
			_ = store.Close()
			if session.ID != "" &&
				session.ID != previousSessionID &&
				session.Status == domain.OnlineSessionReady {
				return nil
			}
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func activeSessionID(statePath string) string {
	if _, err := vfs.Stat(statePath); err != nil {
		return ""
	}
	store, err := storage.OpenInspection(statePath)
	if err != nil {
		return ""
	}
	defer store.Close() //nolint:errcheck
	return store.CurrentSession().ID
}

func (c Controller) domain() (string, error) {
	if c.UserDomain != "" {
		return c.UserDomain, nil
	}
	current, err := user.Current()
	if err == nil && current.Uid != "" {
		return "gui/" + current.Uid, nil
	}
	return "", errs.NewInternalError(errs.SubtypeUnknown, "resolve current user launchd domain").WithCause(err)
}

func parseLaunchdPID(out string) int {
	match := regexp.MustCompile(`pid = ([0-9]+)`).FindStringSubmatch(out)
	if len(match) != 2 {
		return 0
	}
	pid, _ := strconv.Atoi(match[1])
	return pid
}
