// Package workspace enforces the local filesystem boundary for lark-agent.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
	vfs "github.com/liuchong/lark-agent/internal/fsx"
)

// Scope is the only way agent code may read local workspace files.
type Scope struct {
	configuredRoot string
	realRoot       string
	version        string
	excludes       []string
}

// NewScope validates and resolves the workspace root.
func NewScope(root string) (*Scope, error) {
	return NewScopeWithExcludes(root, nil)
}

// NewScopeWithExcludes validates the root and adds workspace-relative exclude
// patterns to the mandatory built-in secret and dependency exclusions.
func NewScopeWithExcludes(root string, excludes []string) (*Scope, error) {
	if root == "" {
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "--workspace is required").
			WithParam("--workspace").
			WithHint("pass an absolute directory path, for example --workspace /workspace/src/sample-org")
	}
	if !filepath.IsAbs(root) {
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "--workspace must be an absolute path: %s", root).
			WithParam("--workspace")
	}
	clean := filepath.Clean(root)
	info, err := vfs.Stat(clean)
	if err != nil {
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace does not exist or is not readable: %s", clean).
			WithParam("--workspace").
			WithCause(err)
	}
	if !info.IsDir() {
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace must be a directory: %s", clean).
			WithParam("--workspace")
	}
	realRoot, err := vfs.EvalSymlinks(clean)
	if err != nil {
		return nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "resolve workspace symlinks: %s", clean).
			WithParam("--workspace").
			WithCause(err)
	}
	realRoot = filepath.Clean(realRoot)
	sum := sha256.Sum256([]byte(realRoot))
	return &Scope{
		configuredRoot: clean,
		realRoot:       realRoot,
		version:        hex.EncodeToString(sum[:8]),
		excludes:       append([]string(nil), excludes...),
	}, nil
}

// Snapshot returns a serializable view of the boundary.
func (s *Scope) Snapshot() domain.WorkspaceScope {
	return domain.WorkspaceScope{
		ConfiguredRoot: s.configuredRoot,
		RealRoot:       s.realRoot,
		Version:        s.version,
		CreatedAt:      time.Now().UTC(),
	}
}

// ConfiguredRoot returns the user-supplied root after filepath.Clean.
func (s *Scope) ConfiguredRoot() string { return s.configuredRoot }

// RealRoot returns the symlink-resolved root.
func (s *Scope) RealRoot() string { return s.realRoot }

// ResolveReadPath converts a workspace-relative path to a real path and rejects
// any path that escapes the workspace through absolute paths, parent traversal,
// or symlinks.
func (s *Scope) ResolveReadPath(rel string) (string, error) {
	if s == nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "workspace scope is nil")
	}
	if rel == "" {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is required").
			WithParam("--path")
	}
	if filepath.IsAbs(rel) {
		return "", outsideWorkspaceError(rel)
	}
	cleanRel := filepath.Clean(rel)
	if cleanRel == "." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) || cleanRel == ".." {
		return "", outsideWorkspaceError(rel)
	}
	if s.shouldExcludePath(cleanRel) {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is excluded: %s", rel).
			WithParam("--path").
			WithHint("secret, credential, dependency, build, and VCS files are not available to the agent")
	}
	candidate := filepath.Join(s.realRoot, cleanRel)
	realCandidate, err := vfs.EvalSymlinks(candidate)
	if err != nil {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is not readable: %s", rel).
			WithParam("--path").
			WithCause(err)
	}
	realCandidate = filepath.Clean(realCandidate)
	if !containsPath(s.realRoot, realCandidate) {
		return "", outsideWorkspaceError(rel)
	}
	return realCandidate, nil
}

// ReadText reads a bounded text file inside the workspace.
func (s *Scope) ReadText(rel string, maxBytes int64) ([]byte, domain.SourceRef, error) {
	path, err := s.ResolveReadPath(rel)
	if err != nil {
		return nil, domain.SourceRef{}, err
	}
	info, err := vfs.Stat(path)
	if err != nil {
		return nil, domain.SourceRef{}, errs.NewInternalError(errs.SubtypeFileIO, "stat workspace file: %s", rel).WithCause(err)
	}
	if info.IsDir() {
		return nil, domain.SourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is a directory: %s", rel).
			WithParam("--path")
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, domain.SourceRef{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace file is too large: %s", rel).
			WithParam("--path")
	}
	data, err := vfs.ReadFile(path)
	if err != nil {
		return nil, domain.SourceRef{}, errs.NewInternalError(errs.SubtypeFileIO, "read workspace file: %s", rel).WithCause(err)
	}
	source := domain.SourceRef{
		RelativePath: filepath.ToSlash(filepath.Clean(rel)),
		Digest:       digest(data),
		Kind:         "workspace_file",
	}
	return data, source, nil
}

// SearchResult is a bounded workspace text search hit.
type SearchResult struct {
	Source  domain.SourceRef `json:"source" yaml:"source"`
	Snippet string           `json:"snippet" yaml:"snippet"`
}

// SearchOptions bounds one workspace search.
type SearchOptions struct {
	Query          string `json:"query" yaml:"query"`
	Path           string `json:"path,omitempty" yaml:"path,omitempty"`
	MaxResults     int    `json:"max_results" yaml:"max_results"`
	MaxFiles       int    `json:"max_files" yaml:"max_files"`
	MaxDirectories int    `json:"max_directories" yaml:"max_directories"`
}

// SearchReport records bounded search output and whether the scan stopped early.
type SearchReport struct {
	Results            []SearchResult `json:"results" yaml:"results"`
	Truncated          bool           `json:"truncated" yaml:"truncated"`
	FilesScanned       int            `json:"files_scanned" yaml:"files_scanned"`
	DirectoriesScanned int            `json:"directories_scanned" yaml:"directories_scanned"`
}

// SearchText performs a small deterministic search inside the workspace. It is
// intentionally simple: the model receives only short snippets and must use a
// read tool for deeper context.
func (s *Scope) SearchText(query string, maxResults int) ([]SearchResult, error) {
	report, err := s.SearchTextReport(SearchOptions{Query: query, MaxResults: maxResults})
	if err != nil {
		return nil, err
	}
	return report.Results, nil
}

// SearchTextReport performs a bounded deterministic search inside the workspace.
func (s *Scope) SearchTextReport(options SearchOptions) (SearchReport, error) {
	return s.SearchTextReportContext(context.Background(), options)
}

// SearchTextReportContext performs a bounded deterministic search and honors
// caller cancellation.
func (s *Scope) SearchTextReportContext(ctx context.Context, options SearchOptions) (SearchReport, error) {
	query := strings.TrimSpace(strings.ToLower(options.Query))
	if query == "" || options.MaxResults <= 0 {
		return SearchReport{}, nil
	}
	if options.MaxFiles <= 0 {
		options.MaxFiles = 2000
	}
	if options.MaxDirectories <= 0 {
		options.MaxDirectories = 600
	}
	searchRoot := s.realRoot
	searchRelativeRoot := "."
	if requestedPath := strings.TrimSpace(options.Path); requestedPath != "" {
		resolved, err := s.ResolveReadPath(requestedPath)
		if err != nil {
			return SearchReport{}, err
		}
		info, err := vfs.Stat(resolved)
		if err != nil {
			return SearchReport{}, errs.NewInternalError(
				errs.SubtypeFileIO,
				"inspect workspace search path: %s",
				requestedPath,
			).WithCause(err)
		}
		if !info.IsDir() {
			return SearchReport{}, errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"search_workspace path must be a directory: %s",
				requestedPath,
			).WithParam("path")
		}
		searchRoot = resolved
		searchRelativeRoot = filepath.ToSlash(filepath.Clean(requestedPath))
	}
	report := SearchReport{}
	if err := s.searchDir(ctx, searchRoot, searchRelativeRoot, query, options, &report); err != nil {
		return SearchReport{}, err
	}
	return report, nil
}

// WalkRules returns AGENTS.md and .agents entries that are within the boundary.
func (s *Scope) WalkRules() ([]domain.SourceRef, error) {
	var refs []domain.SourceRef
	for _, rel := range []string{"AGENTS.md", ".agents"} {
		path, err := s.ResolveReadPath(rel)
		if err != nil {
			continue
		}
		info, err := vfs.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			data, err := vfs.ReadFile(path)
			if err != nil {
				return nil, errs.NewInternalError(errs.SubtypeFileIO, "read rule file: %s", rel).WithCause(err)
			}
			refs = append(refs, domain.SourceRef{RelativePath: rel, Digest: digest(data), Kind: "rule"})
			continue
		}
		if err := s.walkDir(path, rel, &refs); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func (s *Scope) walkDir(root, relRoot string, refs *[]domain.SourceRef) error {
	entries, err := vfs.ReadDir(root)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeFileIO, "read rule directory: %s", relRoot).WithCause(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		rel := filepath.Join(relRoot, name)
		path := filepath.Join(root, name)
		if s.shouldExcludePath(rel) || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if entry.IsDir() {
			if err := s.walkDir(path, rel, refs); err != nil {
				return err
			}
			continue
		}
		data, err := vfs.ReadFile(path)
		if err != nil {
			return errs.NewInternalError(errs.SubtypeFileIO, "read rule file: %s", rel).WithCause(err)
		}
		*refs = append(*refs, domain.SourceRef{RelativePath: filepath.ToSlash(rel), Digest: digest(data), Kind: "rule"})
	}
	return nil
}

func (s *Scope) searchDir(ctx context.Context, absDir, relDir, query string, options SearchOptions, report *SearchReport) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(report.Results) >= options.MaxResults || report.FilesScanned >= options.MaxFiles ||
		report.DirectoriesScanned >= options.MaxDirectories {
		report.Truncated = true
		return nil
	}
	report.DirectoriesScanned++
	entries, err := vfs.ReadDir(absDir)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeFileIO, "search workspace directory: %s", relDir).WithCause(err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(report.Results) >= options.MaxResults || report.FilesScanned >= options.MaxFiles ||
			report.DirectoriesScanned >= options.MaxDirectories {
			report.Truncated = true
			return nil
		}
		name := entry.Name()
		rel := filepath.Join(relDir, name)
		if relDir == "." {
			rel = name
		}
		if s.shouldExcludePath(rel) || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		abs := filepath.Join(absDir, name)
		if entry.IsDir() {
			if err := s.searchDir(ctx, abs, rel, query, options, report); err != nil {
				return err
			}
			continue
		}
		report.FilesScanned++
		info, err := vfs.Stat(abs)
		if err != nil || info.Size() > 64*1024 {
			continue
		}
		data, err := vfs.ReadFile(abs)
		if err != nil || looksBinary(data) {
			continue
		}
		lower := strings.ToLower(string(data))
		idx := workspaceSearchIndex(lower, query)
		if idx < 0 {
			continue
		}
		start := idx - 80
		if start < 0 {
			start = 0
		}
		end := idx + len(query) + 160
		if end > len(data) {
			end = len(data)
		}
		report.Results = append(report.Results, SearchResult{
			Source:  domain.SourceRef{RelativePath: filepath.ToSlash(rel), Digest: digest(data), Kind: "workspace_search"},
			Snippet: string(data[start:end]),
		})
	}
	return nil
}

func workspaceSearchIndex(content, query string) int {
	if index := strings.Index(content, query); index >= 0 {
		return index
	}
	terms := strings.Fields(query)
	if len(terms) < 2 {
		return -1
	}
	first := len(content)
	for _, term := range terms {
		index := strings.Index(content, term)
		if index < 0 {
			return -1
		}
		if index < first {
			first = index
		}
	}
	return first
}

func containsPath(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func outsideWorkspaceError(path string) error {
	return errs.NewValidationError(errs.SubtypeInvalidArgument, "path is outside workspace: %s", path).
		WithParam("--path").
		WithHint("use a relative path inside the configured workspace")
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(sum[:8]))
}

func shouldExcludePath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/")
	for _, part := range parts {
		lower := strings.ToLower(part)
		switch lower {
		case ".git", ".ssh", ".aws", ".gnupg", ".kube", "certs", "certificates", "node_modules", "vendor", "dist", "build", "_worktrees":
			return true
		case ".npmrc", ".pypirc", ".netrc", "credentials", "credentials.json", "secrets.json", "secrets.yaml", "secrets.yml":
			return true
		}
		if strings.HasPrefix(lower, ".env") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "credential") ||
			strings.HasSuffix(lower, ".pem") ||
			strings.HasSuffix(lower, ".key") ||
			strings.HasSuffix(lower, ".p12") ||
			strings.HasSuffix(lower, ".pfx") ||
			strings.HasSuffix(lower, ".crt") ||
			strings.HasSuffix(lower, ".cer") ||
			strings.HasSuffix(lower, ".der") {
			return true
		}
	}
	return false
}

func (s *Scope) shouldExcludePath(rel string) bool {
	if shouldExcludePath(rel) {
		return true
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	for _, pattern := range s.excludes {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if matched, _ := filepath.Match(pattern, clean); matched {
			return true
		}
		for _, part := range strings.Split(clean, "/") {
			if matched, _ := filepath.Match(pattern, part); matched {
				return true
			}
		}
	}
	return false
}

func looksBinary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}
