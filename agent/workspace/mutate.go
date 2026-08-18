package workspace

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/liuchong/lark-agent/agent/domain"
	"github.com/liuchong/lark-agent/internal/apperr"
	vfs "github.com/liuchong/lark-agent/internal/fsx"
)

const maxMutationBytes = 1024 * 1024

// TextEdit is one exact unique replacement against the original file bytes.
type TextEdit struct {
	OldText string
	NewText string
}

type appliedEdit struct {
	OldText string
	NewText string
	Start   int
	End     int
}

// MutationReport is the durable result of one workspace file mutation.
type MutationReport struct {
	Path           string `json:"path"`
	Digest         string `json:"digest"`
	PreviousDigest string `json:"previous_digest,omitempty"`
	Diff           string `json:"diff"`
	Created        bool   `json:"created,omitempty"`
	Replacements   int    `json:"replacements,omitempty"`
}

func (s *Scope) withFileMutation(rel string, fn func() error) error {
	key := filepath.ToSlash(filepath.Clean(rel))
	s.mutationMu.Lock()
	if s.fileMus == nil {
		s.fileMus = map[string]*sync.Mutex{}
	}
	mu, ok := s.fileMus[key]
	if !ok {
		mu = &sync.Mutex{}
		s.fileMus[key] = mu
	}
	s.mutationMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

// ResolveWritePath converts a workspace-relative path for create or overwrite
// and rejects escapes, excluded paths, and symlink walks out of the root.
func (s *Scope) ResolveWritePath(rel string) (string, error) {
	if s == nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "workspace scope is nil")
	}
	if strings.TrimSpace(rel) == "" {
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
	resolved, err := vfs.ResolveWithin(s.realRoot, candidate)
	if err != nil {
		return "", outsideWorkspaceError(rel)
	}
	return resolved, nil
}

// EditText applies exact unique replacements against the original file.
func (s *Scope) EditText(rel string, edits []TextEdit) (report MutationReport, source domain.SourceRef, err error) {
	err = s.withFileMutation(rel, func() error {
		path, resolveErr := s.ResolveWritePath(rel)
		if resolveErr != nil {
			return resolveErr
		}
		info, statErr := vfs.Lstat(path)
		if statErr != nil {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is not readable: %s", rel).
				WithParam("--path").
				WithCause(statErr)
		}
		if info.IsDir() {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is a directory: %s", rel).
				WithParam("--path")
		}
		if info.Size() > maxMutationBytes {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace file is too large: %s", rel).
				WithParam("--path")
		}
		data, readErr := vfs.ReadFile(path)
		if readErr != nil {
			return errs.NewInternalError(errs.SubtypeFileIO, "read workspace file: %s", rel).WithCause(readErr)
		}
		if looksBinary(data) {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is not a text file: %s", rel).
				WithParam("--path")
		}
		original := string(data)
		updated, applied, applyErr := applyTextEdits(original, edits)
		if applyErr != nil {
			return applyErr
		}
		if writeErr := atomicWriteFile(path, []byte(updated), info.Mode().Perm()); writeErr != nil {
			return writeErr
		}
		slash := filepath.ToSlash(filepath.Clean(rel))
		report = MutationReport{
			Path:           slash,
			Digest:         digest([]byte(updated)),
			PreviousDigest: digest(data),
			Diff:           unifiedReplacementDiff(slash, original, applied),
			Replacements:   len(applied),
		}
		source = domain.SourceRef{RelativePath: slash, Digest: report.Digest, Kind: "workspace_file"}
		return nil
	})
	return report, source, err
}

// WriteText creates or overwrites one workspace text file.
func (s *Scope) WriteText(rel, content string) (report MutationReport, source domain.SourceRef, err error) {
	if len(content) > maxMutationBytes {
		return MutationReport{}, domain.SourceRef{}, errs.NewValidationError(
			errs.SubtypeInvalidArgument,
			"workspace write content is too large: %s",
			rel,
		).WithParam("content")
	}
	err = s.withFileMutation(rel, func() error {
		path, resolveErr := s.ResolveWritePath(rel)
		if resolveErr != nil {
			return resolveErr
		}
		previous := ""
		created := true
		perm := os.FileMode(0o644)
		if info, statErr := vfs.Lstat(path); statErr == nil {
			if info.IsDir() {
				return errs.NewValidationError(errs.SubtypeInvalidArgument, "workspace path is a directory: %s", rel).
					WithParam("--path")
			}
			created = false
			perm = info.Mode().Perm()
			if info.Size() <= maxMutationBytes {
				if data, readErr := vfs.ReadFile(path); readErr == nil {
					previous = string(data)
				}
			}
		}
		if writeErr := atomicWriteFile(path, []byte(content), perm); writeErr != nil {
			return writeErr
		}
		slash := filepath.ToSlash(filepath.Clean(rel))
		report = MutationReport{
			Path:    slash,
			Digest:  digest([]byte(content)),
			Diff:    unifiedFileDiff(slash, previous, content),
			Created: created,
		}
		if !created {
			report.PreviousDigest = digest([]byte(previous))
		}
		source = domain.SourceRef{RelativePath: slash, Digest: report.Digest, Kind: "workspace_file"}
		return nil
	})
	return report, source, err
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if err := vfs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.NewInternalError(errs.SubtypeFileIO, "create workspace parent directory").WithCause(err)
	}
	if err := vfs.AtomicWrite(path, data, perm); err != nil {
		return errs.NewInternalError(errs.SubtypeFileIO, "write workspace file").WithCause(err)
	}
	return nil
}

func applyTextEdits(original string, edits []TextEdit) (string, []appliedEdit, error) {
	if len(edits) == 0 {
		return "", nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "edit_workspace edits are required").
			WithParam("edits")
	}
	applied := make([]appliedEdit, 0, len(edits))
	for _, edit := range edits {
		if edit.OldText == "" {
			return "", nil, errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"edit_workspace old_text is required",
			).WithParam("edits")
		}
		start := strings.Index(original, edit.OldText)
		if start < 0 {
			return "", nil, errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"edit_workspace old_text was not found",
			).WithParam("edits")
		}
		rest := original[start+len(edit.OldText):]
		if strings.Contains(rest, edit.OldText) {
			return "", nil, errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"edit_workspace old_text is not unique",
			).WithParam("edits")
		}
		applied = append(applied, appliedEdit{
			OldText: edit.OldText,
			NewText: edit.NewText,
			Start:   start,
			End:     start + len(edit.OldText),
		})
	}
	sort.Slice(applied, func(i, j int) bool {
		return applied[i].Start < applied[j].Start
	})
	for index := 1; index < len(applied); index++ {
		if applied[index].Start < applied[index-1].End {
			return "", nil, errs.NewValidationError(
				errs.SubtypeInvalidArgument,
				"edit_workspace replacements overlap",
			).WithParam("edits")
		}
	}
	var builder strings.Builder
	cursor := 0
	for _, edit := range applied {
		builder.WriteString(original[cursor:edit.Start])
		builder.WriteString(edit.NewText)
		cursor = edit.End
	}
	builder.WriteString(original[cursor:])
	return builder.String(), applied, nil
}
