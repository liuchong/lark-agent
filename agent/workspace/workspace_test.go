package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScopeRequiresAbsoluteRoot(t *testing.T) {
	_, err := NewScope("relative")
	if err == nil {
		t.Fatal("NewScope accepted relative root")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScopeRejectsParentTraversal(t *testing.T) {
	root := t.TempDir()
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.ResolveReadPath("../outside.txt"); err == nil {
		t.Fatal("ResolveReadPath accepted parent traversal")
	}
}

func TestScopeRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.ResolveReadPath("escape/secret.txt"); err == nil {
		t.Fatal("ResolveReadPath accepted symlink escape")
	}
}

func TestScopeReadsAllowedText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("rules"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	content, source, err := scope.ReadText("AGENTS.md", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "rules" || source.RelativePath != "AGENTS.md" {
		t.Fatalf("content=%q source=%+v", content, source)
	}
}

func TestSearchTextSkipsSecretFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "project.md"), []byte("alpha project context"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("alpha SECRET=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	results, err := scope.SearchText("alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%+v", results)
	}
	if results[0].Source.RelativePath != "project.md" {
		t.Fatalf("unexpected source: %+v", results[0])
	}
}

func TestSearchTextReportSkipsWorktrees(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "_worktrees", "other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "_worktrees", "other", "hit.txt"), []byte("needle"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := scope.SearchTextReport(SearchOptions{
		Query:          "needle",
		MaxResults:     10,
		MaxFiles:       100,
		MaxDirectories: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Truncated || len(report.Results) != 0 {
		t.Fatalf("report=%+v", report)
	}
}

func TestSearchTextReportIsScanBounded(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		dir := filepath.Join(root, "bulk", string(rune('a'+i)))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("ordinary content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	report, err := scope.SearchTextReport(SearchOptions{
		Query:          "needle",
		MaxResults:     10,
		MaxFiles:       5,
		MaxDirectories: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Truncated || report.FilesScanned > 5 || len(report.Results) != 0 {
		t.Fatalf("report=%+v", report)
	}
}

func TestSearchTextReportHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hit.txt"), []byte("needle"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = scope.SearchTextReportContext(ctx, SearchOptions{Query: "needle", MaxResults: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestReadTextRejectsCredentialFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ssh", "id_rsa"), []byte("private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := scope.ReadText(".ssh/id_rsa", 1024); err == nil {
		t.Fatal("ReadText accepted excluded credential path")
	}
}

func TestReadTextRejectsCertificateDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "certs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "certs", "client.der"), []byte("certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := scope.ReadText("certs/client.der", 1024); err == nil {
		t.Fatal("ReadText accepted certificate path")
	}
}

func TestListDirectoryIsDepthBoundedAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "service", "internal", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service", "internal", "repo.go"), []byte("package internal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service", "internal", "deep", "skip.go"), []byte("package deep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
			t.Fatal(err)
		}
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := scope.ListDirectory(DirectoryOptions{MaxDepth: 3, MaxEntries: 100, MaxPerDir: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDirectoryEntry(snapshot.Entries, "service/internal", "dir") {
		t.Fatalf("entries=%+v", snapshot.Entries)
	}
	if hasDirectoryEntry(snapshot.Entries, "service/internal/deep/skip.go", "file") ||
		hasDirectoryEntry(snapshot.Entries, "escape", "dir") {
		t.Fatalf("unsafe or too-deep entries=%+v", snapshot.Entries)
	}
}

func TestScopeAppliesConfiguredExcludes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generated", "data.txt"), []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScopeWithExcludes(root, []string{"generated"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := scope.ReadText("generated/data.txt", 1024); err == nil {
		t.Fatal("ReadText accepted configured excluded path")
	}
}

func TestReadTextRangeReturnsLineSliceAndWholeFileDigest(t *testing.T) {
	root := t.TempDir()
	content := "one\ntwo\nthree\nfour\n"
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	full, source, err := scope.ReadText("notes.txt", 1024)
	if err != nil {
		t.Fatal(err)
	}
	report, err := scope.ReadTextRange(ReadOptions{Path: "notes.txt", MaxBytes: 1024, Offset: 2, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if report.Content != "two\nthree" || report.StartLine != 2 || report.EndLine != 3 {
		t.Fatalf("report=%+v", report)
	}
	if report.Source.Digest != source.Digest || string(full) != content {
		t.Fatalf("digest=%s want=%s", report.Source.Digest, source.Digest)
	}
}

func TestSearchTextReportHonorsGlobRegexLiteralAndContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "service"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service", "router.go"), []byte("package service\nfunc RateLimit() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service", "notes.md"), []byte("RateLimit is documented here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	globbed, err := scope.SearchTextReport(SearchOptions{Query: "RateLimit", Glob: "**/*.go", MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(globbed.Results) != 1 || globbed.Results[0].Source.RelativePath != "service/router.go" {
		t.Fatalf("globbed=%+v", globbed)
	}
	regex, err := scope.SearchTextReport(SearchOptions{Query: `func RateLimit\(\)`, Regex: true, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(regex.Results) != 1 || regex.Results[0].Line != 2 {
		t.Fatalf("regex=%+v", regex)
	}
	contexted, err := scope.SearchTextReport(SearchOptions{Query: "RateLimit()", Literal: true, ContextLines: 1, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(contexted.Results) != 1 || !strings.Contains(contexted.Results[0].Snippet, "package service") {
		t.Fatalf("contexted=%+v", contexted)
	}
}

func TestListDirectoryGlobStaysInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "service", "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service", "router.go"), []byte("package service"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service", "internal", "repo.go"), []byte("package internal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "service", "readme.md"), []byte("docs"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := scope.ListDirectory(DirectoryOptions{Glob: "**/*.go", MaxEntries: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDirectoryEntry(snapshot.Entries, "service/router.go", "file") ||
		!hasDirectoryEntry(snapshot.Entries, "service/internal/repo.go", "file") ||
		hasDirectoryEntry(snapshot.Entries, "service/readme.md", "file") {
		t.Fatalf("entries=%+v", snapshot.Entries)
	}
}

func hasDirectoryEntry(entries []DirectoryEntry, path, kind string) bool {
	for _, entry := range entries {
		if entry.Path == path && entry.Kind == kind {
			return true
		}
	}
	return false
}
