package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/liuchong/lark-agent/agent/workspace"
	"github.com/liuchong/lark-agent/internal/apperr"
)

const (
	maxGitHistoryCommits = 20
	maxGitHistoryBytes   = 8 * 1024
	maxGitMetadataFiles  = 50_000
)

// GitDefinitions exposes bounded local Git evidence without network or writes.
func GitDefinitions(scope *workspace.Scope) []Definition {
	return []Definition{{
		Info: toolInfo(
			"inspect_git_history",
			"Inspect bounded local commit history for one repository inside the configured workspace. This is read-only, does not contact remotes, and returns at most 20 commits and 8 KiB.",
			map[string]*schema.ParameterInfo{
				"path":        {Type: schema.String, Desc: "Workspace-relative repository path; empty means the workspace root."},
				"max_commits": {Type: schema.Integer},
			},
		),
		Permission:       ToolPermissionAllow,
		Risk:             ToolRiskLow,
		NonOwnerReadOnly: true,
		Execute: func(ctx context.Context, raw json.RawMessage) (Execution, error) {
			var args struct {
				Path       string `json:"path"`
				MaxCommits int    `json:"max_commits"`
			}
			if err := decodeArgs(raw, &args); err != nil {
				return Execution{}, err
			}
			if args.MaxCommits <= 0 || args.MaxCommits > maxGitHistoryCommits {
				args.MaxCommits = maxGitHistoryCommits
			}
			repository, relativePath, err := resolveGitRepository(scope, args.Path)
			if err != nil {
				return Execution{}, err
			}
			commits, err := readGitHistory(ctx, repository, args.MaxCommits)
			if err != nil {
				return Execution{}, err
			}
			report := gitHistoryReport{
				Path:       relativePath,
				MaxCommits: args.MaxCommits,
				Commits:    commits,
			}
			content, err := json.Marshal(report)
			if err != nil {
				return Execution{}, errs.NewInternalError(errs.SubtypeUnknown, "encode local git history").WithCause(err)
			}
			if len(content) > maxGitHistoryBytes {
				return Execution{}, errs.NewInternalError(
					errs.SubtypeInvalidResponse,
					"bounded local git history exceeded %d bytes",
					maxGitHistoryBytes,
				)
			}
			return Execution{Content: string(content)}, nil
		},
	}}
}

type gitHistoryReport struct {
	Path       string      `json:"path"`
	MaxCommits int         `json:"max_commits"`
	Commits    []gitCommit `json:"commits"`
}

type gitCommit struct {
	Hash    string `json:"hash"`
	Date    string `json:"date"`
	Author  string `json:"author"`
	Subject string `json:"subject"`
}

func resolveGitRepository(scope *workspace.Scope, requested string) (string, string, error) {
	if scope == nil {
		return "", "", errs.NewInternalError(errs.SubtypeUnknown, "workspace scope is nil")
	}
	requested = strings.TrimSpace(requested)
	repository := scope.RealRoot()
	relativePath := "."
	if requested != "" && filepath.Clean(requested) != "." {
		var err error
		repository, err = scope.ResolveReadPath(requested)
		if err != nil {
			return "", "", err
		}
		relativePath = filepath.ToSlash(filepath.Clean(requested))
	}
	info, err := os.Stat(repository)
	if err != nil || !info.IsDir() {
		return "", "", errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"git repository path is not a readable workspace directory: %s",
			requested,
		).WithCause(err)
	}
	metadataRoot, err := resolveGitMetadata(scope.RealRoot(), repository)
	if err != nil {
		return "", "", err
	}
	if err := validateGitMetadata(scope.RealRoot(), metadataRoot); err != nil {
		return "", "", err
	}
	return repository, relativePath, nil
}

func resolveGitMetadata(workspaceRoot, repository string) (string, error) {
	gitPath := filepath.Join(repository, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is not a Git repository").WithCause(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", outsideGitMetadataError(gitPath)
	}
	if info.IsDir() {
		realPath, err := filepath.EvalSymlinks(gitPath)
		if err != nil || !pathWithinRoot(workspaceRoot, realPath) {
			return "", outsideGitMetadataError(gitPath)
		}
		return realPath, nil
	}
	if !info.Mode().IsRegular() || info.Size() > 4*1024 {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "unsupported Git metadata file")
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "Git metadata is not readable").WithCause(err)
	}
	value := strings.TrimSpace(string(data))
	if !strings.HasPrefix(value, "gitdir:") {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid Git metadata file")
	}
	target := strings.TrimSpace(strings.TrimPrefix(value, "gitdir:"))
	if !filepath.IsAbs(target) {
		target = filepath.Join(repository, target)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil || !pathWithinRoot(workspaceRoot, realTarget) {
		return "", outsideGitMetadataError(target)
	}
	return realTarget, nil
}

func validateGitMetadata(workspaceRoot, metadataRoot string) error {
	roots := []string{metadataRoot}
	if commonDir, ok, err := gitPointerPath(metadataRoot, "commondir"); err != nil {
		return err
	} else if ok {
		roots = append(roots, commonDir)
	}
	seenRoots := make(map[string]bool, len(roots))
	visited := 0
	for _, root := range roots {
		root, err := filepath.EvalSymlinks(root)
		if err != nil || !pathWithinRoot(workspaceRoot, root) {
			return outsideGitMetadataError(root)
		}
		if seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			visited++
			if visited > maxGitMetadataFiles {
				return errs.NewValidationError(
					errs.SubtypeFailedPrecondition,
					"Git metadata inspection exceeded %d entries",
					maxGitMetadataFiles,
				)
			}
			if entry.Type()&os.ModeSymlink == 0 {
				return nil
			}
			realPath, err := filepath.EvalSymlinks(path)
			if err != nil || !pathWithinRoot(workspaceRoot, realPath) {
				return outsideGitMetadataError(path)
			}
			return nil
		})
		if err != nil {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "Git metadata is not safely readable").WithCause(err)
		}
		if err := validateGitAlternates(workspaceRoot, root); err != nil {
			return err
		}
	}
	return nil
}

func gitPointerPath(root, name string) (string, bool, error) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4*1024 {
		return "", false, errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid Git %s metadata", name).WithCause(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, errs.NewValidationError(errs.SubtypeInvalidArgument, "Git %s metadata is not readable", name).WithCause(err)
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return "", false, errs.NewValidationError(errs.SubtypeInvalidArgument, "empty Git %s metadata", name)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false, errs.NewValidationError(errs.SubtypeInvalidArgument, "Git %s metadata is not readable", name).WithCause(err)
	}
	return realTarget, true, nil
}

func validateGitAlternates(workspaceRoot, metadataRoot string) error {
	httpAlternates := filepath.Join(metadataRoot, "objects", "info", "http-alternates")
	if data, err := os.ReadFile(httpAlternates); err == nil && strings.TrimSpace(string(data)) != "" {
		return errs.NewValidationError(errs.SubtypeFailedPrecondition, "network Git alternates are not allowed")
	} else if err != nil && !os.IsNotExist(err) {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "Git HTTP alternates are not readable").WithCause(err)
	}
	alternates := filepath.Join(metadataRoot, "objects", "info", "alternates")
	data, err := os.ReadFile(alternates)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "Git alternates are not readable").WithCause(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		target := strings.TrimSpace(line)
		if target == "" {
			continue
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(metadataRoot, "objects", target)
		}
		realTarget, err := filepath.EvalSymlinks(target)
		if err != nil || !pathWithinRoot(workspaceRoot, realTarget) {
			return outsideGitMetadataError(target)
		}
	}
	return nil
}

func readGitHistory(ctx context.Context, repository string, maxCommits int) ([]gitCommit, error) {
	command := exec.CommandContext(
		ctx,
		"git",
		"--no-pager",
		"--no-replace-objects",
		"-C",
		repository,
		"log",
		"--no-ext-diff",
		"--max-count="+strconv.Itoa(maxCommits),
		"--format=%H%n%aI%n%<(80,trunc)%an%n%<(160,trunc)%s%x00",
	)
	command.Env = append(gitSafeBaseEnvironment(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
	)
	var stdout gitBoundedBuffer
	stdout.limit = maxGitHistoryBytes
	var stderr gitBoundedBuffer
	stderr.limit = 2 * 1024
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, errs.NewValidationError(
			errs.SubtypeFailedPrecondition,
			"read local Git history: %s",
			strings.TrimSpace(stderr.String()),
		).WithCause(err)
	}
	records := bytes.Split(stdout.Bytes(), []byte{0})
	commits := make([]gitCommit, 0, min(maxCommits, len(records)))
	for _, record := range records {
		record = bytes.TrimSpace(record)
		if len(record) == 0 {
			continue
		}
		fields := strings.SplitN(string(record), "\n", 4)
		if len(fields) != 4 {
			return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "Git returned malformed local history")
		}
		commits = append(commits, gitCommit{
			Hash:    strings.TrimSpace(fields[0]),
			Date:    strings.TrimSpace(fields[1]),
			Author:  strings.TrimSpace(fields[2]),
			Subject: strings.TrimSpace(fields[3]),
		})
		if len(commits) == maxCommits {
			break
		}
	}
	return commits, nil
}

func gitSafeBaseEnvironment() []string {
	environment := os.Environ()
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		if strings.HasPrefix(strings.ToUpper(item), "GIT_") {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

type gitBoundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *gitBoundedBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		remaining := b.limit - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(data[:remaining])
		}
		return len(data), errs.NewValidationError(errs.SubtypeFailedPrecondition, "Git output exceeded its byte limit")
	}
	return b.Buffer.Write(data)
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func outsideGitMetadataError(path string) error {
	return errs.NewValidationError(
		errs.SubtypeFailedPrecondition,
		"Git metadata resolves outside the configured workspace: %s",
		path,
	)
}
