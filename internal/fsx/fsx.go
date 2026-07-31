package fsx

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempPath := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}
	defer cleanup()

	if err := file.Chmod(perm); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace destination: %w", err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent directory: %w", err)
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	if syncErr != nil {
		return fmt.Errorf("sync parent directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close parent directory: %w", closeErr)
	}
	return nil
}

func ResolveWithin(root, candidate string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root symlinks: %w", err)
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve candidate path: %w", err)
	}
	if !isWithin(rootAbs, candidateAbs) {
		return "", fmt.Errorf("path escapes workspace: %s", candidate)
	}
	candidateReal, err := resolveWithMissingTail(candidateAbs)
	if err != nil {
		return "", err
	}
	if !isWithin(rootReal, candidateReal) {
		return "", fmt.Errorf("path resolves outside workspace: %s", candidate)
	}
	return candidateAbs, nil
}

func resolveWithMissingTail(path string) (string, error) {
	existing := path
	var tail []string
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect path: %w", err)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		tail = append(tail, filepath.Base(existing))
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("resolve path symlinks: %w", err)
	}
	for index := len(tail) - 1; index >= 0; index-- {
		real = filepath.Join(real, tail[index])
	}
	return filepath.Clean(real), nil
}

func isWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func UserHomeDir() (string, error)                 { return os.UserHomeDir() }
func Executable() (string, error)                  { return os.Executable() }
func MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
func ReadFile(path string) ([]byte, error)       { return os.ReadFile(path) }
func Stat(path string) (fs.FileInfo, error)      { return os.Stat(path) }
func Lstat(path string) (fs.FileInfo, error)     { return os.Lstat(path) }
func Remove(path string) error                   { return os.Remove(path) }
func EvalSymlinks(path string) (string, error)   { return filepath.EvalSymlinks(path) }
func ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }
