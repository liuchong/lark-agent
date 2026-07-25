package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/workspace"
	"github.com/liuchong/lark-agent/internal/apperr"
	vfs "github.com/liuchong/lark-agent/internal/fsx"
)

// ShellOptions controls the workspace shell sandbox.
type ShellOptions struct {
	ApprovalRequired     bool
	Approvals            ShellApprovalStore
	MaxTimeout           time.Duration
	MaxOutputBytes       int
	CodingPlanMode       bool
	PlanPath             string
	AllowUnboundedSearch bool
}

// ShellApprovalStore persists and consumes one-time exact-command approvals.
type ShellApprovalStore interface {
	RequestShellApproval(context.Context, string, string, string) (int64, error)
	ConsumeShellApproval(context.Context, string, string, string) (int64, bool, error)
	BeginShellAction(context.Context, string, string, string) (int64, string, bool, error)
	CompleteShellApproval(context.Context, int64, string, string) error
}

type shellResult struct {
	ExitCode         int    `json:"exit_code"`
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	TimedOut         bool   `json:"timed_out"`
	Sandboxed        bool   `json:"sandboxed"`
	ApprovalRequired bool   `json:"approval_required,omitempty"`
	ActionID         int64  `json:"action_id,omitempty"`
	Uncertain        bool   `json:"uncertain,omitempty"`
}

// ShellDefinition executes commands under a macOS Seatbelt workspace boundary.
func ShellDefinition(scope *workspace.Scope, options ShellOptions) Definition {
	if options.MaxTimeout <= 0 {
		options.MaxTimeout = 2 * time.Minute
	}
	if options.MaxOutputBytes <= 0 {
		options.MaxOutputBytes = 64 * 1024
	}
	return Definition{
		Info: &schema.ToolInfo{
			Name: "shell",
			Desc: "Run a shell command inside the workspace sandbox. Local file access is confined to the workspace. Avoid destructive commands and unnecessary external side effects. Do not use shell to send Lark IM messages; submit reply decisions through submit_decision so the runtime renders mentions and audits the send.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"command":         {Type: schema.String, Required: true},
				"cwd":             {Type: schema.String},
				"timeout_seconds": {Type: schema.Integer},
			}),
		},
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Command        string `json:"command"`
				CWD            string `json:"cwd"`
				TimeoutSeconds int    `json:"timeout_seconds"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if strings.TrimSpace(args.Command) == "" {
				return Execution{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "shell command is required")
			}
			if !options.AllowUnboundedSearch && isUnboundedSearchCommand(args.Command) {
				return Execution{}, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"unbounded shell search is not allowed; use bounded code-search tools such as search_workspace, search_code_symbols, trace_code_path, or explore_workspace",
				).WithParam("command")
			}
			if isDirectLarkMessageSendCommand(args.Command) {
				return Execution{}, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"direct Lark IM sends are not allowed from shell; finish with submit_decision so the runtime can render mentions and audit the reply",
				).WithParam("command")
			}
			if options.CodingPlanMode && !isReadOnlyCommand(args.Command) {
				return Execution{}, errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"coding plan mode denies workspace write shell commands; submit the plan and leave plan mode before changing production files",
				).WithParam("command")
			}
			var approvalID int64
			if options.ApprovalRequired && !isReadOnlyCommand(args.Command) {
				if options.Approvals == nil {
					return Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "shell approval store is not configured")
				}
				dedupKey := workItemDedup(ctx)
				if dedupKey == "" {
					return Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "shell approval requires a durable work item")
				}
				approvedID, approved, err := options.Approvals.ConsumeShellApproval(ctx, dedupKey, args.Command, args.CWD)
				if err != nil {
					return Execution{}, err
				}
				if !approved {
					actionID, err := options.Approvals.RequestShellApproval(ctx, dedupKey, args.Command, args.CWD)
					if err != nil {
						return Execution{}, err
					}
					return jsonExecution(shellResult{
						ExitCode:         -1,
						Sandboxed:        true,
						ApprovalRequired: true,
						ActionID:         actionID,
						Stderr:           "approval is required before executing this exact command",
					}, nil, nil)
				}
				approvalID = approvedID
			}
			if approvalID == 0 && options.Approvals != nil {
				dedupKey := workItemDedup(ctx)
				if dedupKey == "" {
					return Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "shell audit requires a durable work item")
				}
				actionID, previous, uncertain, err := options.Approvals.BeginShellAction(ctx, dedupKey, args.Command, args.CWD)
				if err != nil {
					return Execution{}, err
				}
				if previous != "" {
					return Execution{Content: previous}, nil
				}
				if uncertain {
					return jsonExecution(shellResult{
						ExitCode:  -1,
						Sandboxed: true,
						ActionID:  actionID,
						Uncertain: true,
						Stderr:    "a previous execution ended without a recorded result; inspect workspace state before choosing another action",
					}, nil, nil)
				}
				approvalID = actionID
			}
			timeout := time.Duration(args.TimeoutSeconds) * time.Second
			if timeout <= 0 || timeout > options.MaxTimeout {
				timeout = options.MaxTimeout
			}
			result, err := runSandboxedShell(ctx, scope, args.Command, args.CWD, timeout, options.MaxOutputBytes)
			if approvalID != 0 {
				resultJSON, _ := json.Marshal(result)
				errorText := ""
				if err != nil {
					errorText = err.Error()
				}
				if completeErr := options.Approvals.CompleteShellApproval(ctx, approvalID, string(resultJSON), errorText); completeErr != nil {
					return Execution{}, completeErr
				}
			}
			return jsonExecution(result, nil, err)
		},
	}
}

func runSandboxedShell(ctx context.Context, scope *workspace.Scope, command, cwd string, timeout time.Duration, outputBytes int) (shellResult, error) {
	if runtime.GOOS != "darwin" {
		return shellResult{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "workspace shell requires macOS Seatbelt")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return shellResult{}, errs.NewValidationError(errs.SubtypeFailedPrecondition, "sandbox-exec is unavailable; refusing unsandboxed shell execution").WithCause(err)
	}
	workdir := scope.RealRoot()
	if cwd != "" && filepath.Clean(cwd) != "." {
		resolved, err := scope.ResolveReadPath(cwd)
		if err != nil {
			return shellResult{}, err
		}
		info, err := vfs.Stat(resolved)
		if err != nil {
			return shellResult{}, errs.NewInternalError(errs.SubtypeFileIO, "stat shell working directory: %s", cwd).WithCause(err)
		}
		if !info.IsDir() {
			return shellResult{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "shell cwd is not a directory: %s", cwd)
		}
		workdir = resolved
	}
	runtimeRoot := filepath.Join(scope.RealRoot(), ".local", "lark-agent", "runtime")
	home := filepath.Join(runtimeRoot, "home")
	temp := filepath.Join(runtimeRoot, "tmp")
	for _, path := range []string{home, temp} {
		if err := vfs.MkdirAll(path, 0o700); err != nil {
			return shellResult{}, errs.NewInternalError(errs.SubtypeFileIO, "create sandbox runtime directory").WithCause(err)
		}
	}
	deniedPaths, err := scope.SandboxDeniedPaths()
	if err != nil {
		return shellResult{}, err
	}
	profile := seatbeltProfile(scope.RealRoot(), deniedPaths)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/usr/bin/sandbox-exec", "-p", profile, "/bin/bash", "--noprofile", "--norc", "-c", command)
	cmd.Dir = workdir
	cmd.Env = sanitizedShellEnvironment(home, temp)
	var stdout, stderr limitedBuffer
	stdout.max = outputBytes
	stderr.max = outputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	result := shellResult{
		ExitCode:  0,
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		TimedOut:  errors.Is(runCtx.Err(), context.DeadlineExceeded),
		Sandboxed: true,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		if result.TimedOut {
			result.ExitCode = -1
			return result, nil
		}
		return shellResult{}, errs.NewInternalError(errs.SubtypeUnknown, "execute workspace shell").WithCause(err)
	}
	return result, nil
}

func seatbeltProfile(root string, deniedPaths []workspace.SandboxDeniedPath) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(root)
	profile := `(version 1)
(allow default)
(deny file-read*
  (subpath "/Users")
  (subpath "/Volumes")
  (subpath "/private/tmp")
  (subpath "/private/var/folders")
  (subpath "/opt/homebrew/var")
  (subpath "/opt/homebrew/etc")
  (subpath "/usr/local/var")
  (subpath "/usr/local/etc"))
(deny file-write*)
(allow file-read* file-write* (subpath "` + escaped + `"))
`
	if len(deniedPaths) == 0 {
		return profile
	}
	var denied strings.Builder
	denied.WriteString("(deny file-read* file-write*\n")
	for _, deniedPath := range deniedPaths {
		escapedPath := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(deniedPath.Path)
		filter := "literal"
		if deniedPath.IsDirectory {
			filter = "subpath"
		}
		denied.WriteString(`  (` + filter + ` "` + escapedPath + `")` + "\n")
	}
	denied.WriteString(")\n")
	return profile + denied.String()
}

func sanitizedShellEnvironment(home, temp string) []string {
	allowed := map[string]bool{
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TERM": true,
	}
	env := []string{
		"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + home,
		"TMPDIR=" + temp,
	}
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if ok && allowed[key] {
			env = append(env, item)
		}
	}
	return env
}

func isReadOnlyCommand(command string) bool {
	segments := strings.Fields(strings.TrimSpace(command))
	if len(segments) == 0 {
		return false
	}
	base := filepath.Base(segments[0])
	switch base {
	case "pwd", "ls", "find", "rg", "grep", "cat", "head", "tail", "sed", "awk", "wc", "stat", "file", "git", "go":
		return !strings.ContainsAny(command, ">|;&`$()")
	default:
		return false
	}
}

func isUnboundedSearchCommand(command string) bool {
	tokens := shellCommandTokens(command)
	for start := 0; start < len(tokens); start++ {
		if start > 0 && !isShellCommandSeparator(tokens[start-1]) {
			continue
		}
		end := start
		for end < len(tokens) && !isShellCommandSeparator(tokens[end]) {
			end++
		}
		if isUnboundedSearchTokens(tokens[start:end]) {
			return true
		}
	}
	return false
}

func isUnboundedSearchTokens(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	base := filepath.Base(strings.ToLower(tokens[0]))
	if base == "env" {
		index := 1
		for index < len(tokens) && (strings.HasPrefix(tokens[index], "-") || strings.Contains(tokens[index], "=")) {
			index++
		}
		return isUnboundedSearchTokens(tokens[index:])
	}
	if base == "bash" || base == "sh" || base == "zsh" {
		for index := 1; index < len(tokens)-1; index++ {
			if shellOptionRunsCommand(tokens[index]) {
				return isUnboundedSearchCommand(tokens[index+1])
			}
		}
	}
	switch base {
	case "grep":
		for _, token := range tokens[1:] {
			if token == "-r" || token == "-R" || token == "--recursive" || token == "--dereference-recursive" ||
				(strings.HasPrefix(token, "-") && strings.Contains(token, "r")) {
				return true
			}
		}
		return false
	case "find":
		if len(tokens) == 1 {
			return true
		}
		target := tokens[1]
		return target == "." || target == "./" || target == "" || target == "/"
	case "rg":
		targets := rgSearchTargets(tokens[1:])
		if len(targets) == 0 {
			return true
		}
		for _, target := range targets {
			if target == "." || target == "./" || target == "/" {
				return true
			}
		}
	}
	return false
}

func rgSearchTargets(args []string) []string {
	patternSeen := false
	literal := false
	var targets []string
	optionsWithValue := map[string]bool{
		"-g": true, "--glob": true, "-t": true, "--type": true,
		"-T": true, "--type-not": true, "-A": true, "--after-context": true,
		"-B": true, "--before-context": true, "-C": true, "--context": true,
		"-m": true, "--max-count": true, "--max-depth": true,
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" && !literal {
			literal = true
			continue
		}
		if !literal && strings.HasPrefix(arg, "-") {
			option := arg
			if equals := strings.IndexByte(option, '='); equals >= 0 {
				option = option[:equals]
			}
			if optionsWithValue[option] && !strings.Contains(arg, "=") && index+1 < len(args) {
				index++
			}
			continue
		}
		if !patternSeen {
			patternSeen = true
			continue
		}
		if arg != "" && !strings.HasPrefix(arg, "-") {
			targets = append(targets, arg)
		}
	}
	return targets
}

func isDirectLarkMessageSendCommand(command string) bool {
	tokens := shellCommandTokens(command)
	if len(tokens) == 0 {
		return false
	}
	for start := 0; start < len(tokens); start++ {
		if !isShellCommandSeparator(tokens[start]) && (start == 0 || isShellCommandSeparator(tokens[start-1])) {
			if isDirectLarkMessageSendTokens(tokens[start:]) {
				return true
			}
		}
	}
	return false
}

func isDirectLarkMessageSendTokens(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	base := filepath.Base(strings.ToLower(tokens[0]))
	if (base == "bash" || base == "sh") && len(tokens) >= 3 {
		for index := 1; index < len(tokens)-1; index++ {
			if shellOptionRunsCommand(tokens[index]) && isDirectLarkMessageSendCommand(tokens[index+1]) {
				return true
			}
		}
	}
	if base == "go" && len(tokens) >= 4 && tokens[1] == "run" {
		for index := 2; index < len(tokens); index++ {
			if isLarkIMSendArgs(tokens[index:]) {
				return true
			}
		}
	}
	return false
}

func shellOptionRunsCommand(option string) bool {
	return strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && strings.Contains(option, "c")
}

func isLarkIMSendArgs(args []string) bool {
	if len(args) >= 2 && args[0] == "im" {
		switch args[1] {
		case "+messages-send", "+messages-reply":
			return true
		}
	}
	if len(args) >= 3 && args[0] == "api" && strings.EqualFold(args[1], "post") {
		path := args[2]
		return path == "/open-apis/im/v1/messages" ||
			(strings.HasPrefix(path, "/open-apis/im/v1/messages/") && strings.HasSuffix(path, "/reply"))
	}
	return false
}

func isShellCommandSeparator(token string) bool {
	switch token {
	case ";", "&&", "||", "|", "&":
		return true
	default:
		return false
	}
}

func shellCommandTokens(command string) []string {
	var tokens []string
	var current strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for _, char := range command {
		if escaped {
			current.WriteRune(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case ' ', '\t', '\n', '\r':
			flush()
		case ';', '|':
			flush()
			tokens = append(tokens, string(char))
		case '&':
			flush()
			tokens = append(tokens, "&")
		default:
			current.WriteRune(char)
		}
	}
	flush()
	return combineShellSeparators(tokens)
}

func combineShellSeparators(tokens []string) []string {
	if len(tokens) < 2 {
		return tokens
	}
	combined := make([]string, 0, len(tokens))
	for index := 0; index < len(tokens); index++ {
		if index+1 < len(tokens) {
			pair := tokens[index] + tokens[index+1]
			if pair == "&&" || pair == "||" {
				combined = append(combined, pair)
				index++
				continue
			}
		}
		combined = append(combined, tokens[index])
	}
	return combined
}

type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.max - b.buf.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
			b.truncated = true
		}
		_, _ = b.buf.Write(data)
	} else {
		b.truncated = true
	}
	return original, nil
}

func (b *limitedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + "\n... output truncated"
	}
	return b.buf.String()
}
