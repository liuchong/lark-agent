package workspace

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/liuchong/lark-agent/internal/apperr"
	vfs "github.com/liuchong/lark-agent/internal/fsx"
)

// DirectoryOptions bounds a deterministic workspace overview.
type DirectoryOptions struct {
	Path          string
	MaxDepth      int
	MaxEntries    int
	MaxPerDir     int
	IncludeHidden bool
}

// DirectoryEntry is one safe workspace-relative directory overview item.
type DirectoryEntry struct {
	Path string `json:"path" yaml:"path"`
	Kind string `json:"kind" yaml:"kind"`
}

// DirectorySnapshot is a bounded workspace directory overview.
type DirectorySnapshot struct {
	Entries   []DirectoryEntry `json:"entries" yaml:"entries"`
	Truncated bool             `json:"truncated" yaml:"truncated"`
	Omitted   int              `json:"omitted" yaml:"omitted"`
}

// HasGitRepositoryMarker reports a non-symlink .git marker in one directory
// already confined to the workspace.
func (s *Scope) HasGitRepositoryMarker(relative string) bool {
	directory := s.realRoot
	if relative != "" && filepath.Clean(relative) != "." {
		var err error
		directory, err = s.ResolveReadPath(relative)
		if err != nil {
			return false
		}
	}
	info, err := vfs.Lstat(filepath.Join(directory, ".git"))
	if err != nil || info.Mode()&fs.ModeSymlink != 0 {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

// ControlFiles is a bounded index of workspace-local rules and skills.
type ControlFiles struct {
	RuleFiles  []string `json:"rule_files" yaml:"rule_files"`
	SkillFiles []string `json:"skill_files" yaml:"skill_files"`
	Truncated  bool     `json:"truncated" yaml:"truncated"`
}

// SandboxDeniedPath is one existing excluded path that Seatbelt must deny.
type SandboxDeniedPath struct {
	Path        string
	IsDirectory bool
}

// ListDirectory returns a sorted, depth- and count-bounded directory overview.
func (s *Scope) ListDirectory(opts DirectoryOptions) (DirectorySnapshot, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 3
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = 400
	}
	if opts.MaxPerDir <= 0 {
		opts.MaxPerDir = 60
	}
	var snapshot DirectorySnapshot
	type pendingDir struct {
		abs   string
		rel   string
		depth int
	}
	startRel := filepath.Clean(opts.Path)
	startAbs := s.realRoot
	if opts.Path == "" || startRel == "." {
		startRel = "."
	} else {
		var err error
		startAbs, err = s.ResolveReadPath(opts.Path)
		if err != nil {
			return DirectorySnapshot{}, err
		}
		info, err := vfs.Stat(startAbs)
		if err != nil {
			return DirectorySnapshot{}, errs.NewInternalError(errs.SubtypeFileIO, "stat workspace directory: %s", opts.Path).WithCause(err)
		}
		if !info.IsDir() {
			return DirectorySnapshot{}, errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is not a directory: %s", opts.Path).
				WithParam("--path")
		}
	}
	queue := []pendingDir{{abs: startAbs, rel: startRel, depth: 0}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		entries, err := vfs.ReadDir(current.abs)
		if err != nil {
			return DirectorySnapshot{}, errs.NewInternalError(errs.SubtypeFileIO, "list workspace directory: %s", current.rel).WithCause(err)
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() != entries[j].IsDir() {
				return entries[i].IsDir()
			}
			return entries[i].Name() < entries[j].Name()
		})
		visible := make([]fs.DirEntry, 0, len(entries))
		for _, entry := range entries {
			rel := entry.Name()
			if current.rel != "." {
				rel = filepath.Join(current.rel, entry.Name())
			}
			if entry.Type()&fs.ModeSymlink != 0 || s.shouldExcludePath(rel) {
				continue
			}
			if !opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".") && entry.Name() != ".agents" {
				continue
			}
			visible = append(visible, entry)
		}
		if len(visible) > opts.MaxPerDir {
			snapshot.Truncated = true
			snapshot.Omitted += len(visible) - opts.MaxPerDir
			visible = visible[:opts.MaxPerDir]
		}
		for _, entry := range visible {
			if len(snapshot.Entries) >= opts.MaxEntries {
				snapshot.Truncated = true
				snapshot.Omitted++
				continue
			}
			rel := entry.Name()
			if current.rel != "." {
				rel = filepath.Join(current.rel, entry.Name())
			}
			kind := "file"
			if entry.IsDir() {
				kind = "dir"
			}
			snapshot.Entries = append(snapshot.Entries, DirectoryEntry{
				Path: filepath.ToSlash(rel),
				Kind: kind,
			})
			if entry.IsDir() && current.depth+1 < opts.MaxDepth {
				queue = append(queue, pendingDir{
					abs:   filepath.Join(current.abs, entry.Name()),
					rel:   rel,
					depth: current.depth + 1,
				})
			}
		}
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool {
		return snapshot.Entries[i].Path < snapshot.Entries[j].Path
	})
	return snapshot, nil
}

// RuleAndSkillPaths lists bounded rule and skill paths from a snapshot.
func RuleAndSkillPaths(entries []DirectoryEntry) (rules, skills []string) {
	for _, entry := range entries {
		if entry.Kind != "file" {
			continue
		}
		base := filepath.Base(entry.Path)
		if base == "AGENTS.md" || strings.HasPrefix(filepath.ToSlash(entry.Path), ".agents/") {
			rules = append(rules, entry.Path)
		}
		if base == "SKILL.md" && strings.Contains(filepath.ToSlash(entry.Path), "/skills/") {
			skills = append(skills, entry.Path)
		}
	}
	sort.Strings(rules)
	sort.Strings(skills)
	return rules, skills
}

// DiscoverControlFiles indexes nested AGENTS.md and .agents metadata without
// loading their contents into the initial context.
func (s *Scope) DiscoverControlFiles(maxDepth, maxFiles int) (ControlFiles, error) {
	if maxDepth <= 0 {
		maxDepth = 6
	}
	if maxFiles <= 0 {
		maxFiles = 256
	}
	var result ControlFiles
	type pendingDir struct {
		abs   string
		rel   string
		depth int
	}
	queue := []pendingDir{{abs: s.realRoot, rel: ".", depth: 0}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		entries, err := vfs.ReadDir(current.abs)
		if err != nil {
			return ControlFiles{}, errs.NewInternalError(errs.SubtypeFileIO, "discover workspace control files: %s", current.rel).WithCause(err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			rel := entry.Name()
			if current.rel != "." {
				rel = filepath.Join(current.rel, entry.Name())
			}
			if entry.Type()&fs.ModeSymlink != 0 || s.shouldExcludePath(rel) {
				continue
			}
			slashRel := filepath.ToSlash(rel)
			if entry.IsDir() {
				if current.depth+1 < maxDepth &&
					(!strings.HasPrefix(entry.Name(), ".") || entry.Name() == ".agents") {
					queue = append(queue, pendingDir{
						abs:   filepath.Join(current.abs, entry.Name()),
						rel:   rel,
						depth: current.depth + 1,
					})
				}
				continue
			}
			isAgents := entry.Name() == "AGENTS.md"
			inAgentsDir := strings.Contains("/"+slashRel, "/.agents/")
			if !isAgents && !inAgentsDir {
				continue
			}
			if len(result.RuleFiles)+len(result.SkillFiles) >= maxFiles {
				result.Truncated = true
				continue
			}
			if entry.Name() == "SKILL.md" && strings.Contains("/"+slashRel, "/.agents/skills/") {
				result.SkillFiles = append(result.SkillFiles, slashRel)
			} else {
				result.RuleFiles = append(result.RuleFiles, slashRel)
			}
		}
	}
	sort.Strings(result.RuleFiles)
	sort.Strings(result.SkillFiles)
	return result, nil
}

// SandboxDeniedPaths discovers existing excluded paths without descending into
// excluded directories. Shell execution is refused if this scan fails.
func (s *Scope) SandboxDeniedPaths() ([]SandboxDeniedPath, error) {
	var denied []SandboxDeniedPath
	var walk func(string, string) error
	walk = func(absDir, relDir string) error {
		entries, err := vfs.ReadDir(absDir)
		if err != nil {
			return errs.NewInternalError(errs.SubtypeFileIO, "discover sandbox exclusions: %s", relDir).WithCause(err)
		}
		for _, entry := range entries {
			rel := entry.Name()
			if relDir != "." {
				rel = filepath.Join(relDir, entry.Name())
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				continue
			}
			abs := filepath.Join(absDir, entry.Name())
			if s.shouldExcludePath(rel) {
				denied = append(denied, SandboxDeniedPath{Path: abs, IsDirectory: entry.IsDir()})
				continue
			}
			if entry.IsDir() {
				if err := walk(abs, rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(s.realRoot, "."); err != nil {
		return nil, err
	}
	sort.Slice(denied, func(i, j int) bool { return denied[i].Path < denied[j].Path })
	return denied, nil
}
