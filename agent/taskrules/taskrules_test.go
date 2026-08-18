package taskrules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDisabledDoesNotReadFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultFileName), []byte("secret policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := Load(Config{Enabled: false, ConfigDir: dir, Path: DefaultFileName})
	if snap.Status != StatusDisabled || snap.Body != "" || snap.Ready() || snap.Fault() {
		t.Fatalf("snapshot=%+v", snap)
	}
	if strings.Contains(snap.Public().Digest, "secret") || snap.Public().FileName != DefaultFileName {
		t.Fatalf("public=%+v", snap.Public())
	}
}

func TestLoadReadsRelativeFileAndHidesBodyFromPublicView(t *testing.T) {
	dir := t.TempDir()
	body := "Investigate group notices that name a sample-service outage.\n"
	if err := os.WriteFile(filepath.Join(dir, DefaultFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := Load(Config{Enabled: true, ConfigDir: dir, Path: DefaultFileName, MaxBytes: DefaultMaxBytes})
	if !snap.Ready() || snap.Body != body || snap.Digest == "" {
		t.Fatalf("snapshot=%+v", snap)
	}
	view := snap.Public()
	if view.Status != StatusOK || !strings.HasPrefix(view.Digest, "sha256:") || view.FileName != DefaultFileName {
		t.Fatalf("public=%+v", view)
	}
	if strings.Contains(snap.ClassifierProjection(), "sample-service outage") == false {
		t.Fatal("classifier projection omitted private body")
	}
	if strings.Contains(view.Digest, body) {
		t.Fatal("digest leaked body")
	}
}

func TestLoadRejectsEscapedPathAndOversizedFile(t *testing.T) {
	dir := t.TempDir()
	snap := Load(Config{Enabled: true, ConfigDir: dir, Path: "../TASK_RULES.md"})
	if snap.Status != StatusEscaped || snap.Fault() != true {
		t.Fatalf("escaped snapshot=%+v", snap)
	}
	if err := os.WriteFile(filepath.Join(dir, DefaultFileName), []byte(strings.Repeat("a", 8)), 0o600); err != nil {
		t.Fatal(err)
	}
	snap = Load(Config{Enabled: true, ConfigDir: dir, Path: DefaultFileName, MaxBytes: 4})
	if snap.Status != StatusTooLarge || snap.Ready() {
		t.Fatalf("too large snapshot=%+v", snap)
	}
}

func TestLoadMissingEnabledIsFaultAndEmptyIsNot(t *testing.T) {
	dir := t.TempDir()
	missing := Load(Config{Enabled: true, ConfigDir: dir, Path: DefaultFileName})
	if missing.Status != StatusMissing || !missing.Fault() {
		t.Fatalf("missing=%+v", missing)
	}
	if err := os.WriteFile(filepath.Join(dir, DefaultFileName), []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := Load(Config{Enabled: true, ConfigDir: dir, Path: DefaultFileName})
	if empty.Status != StatusEmpty || empty.Fault() || empty.Ready() {
		t.Fatalf("empty=%+v", empty)
	}
}

func TestWriteTemplateIsGenericAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteTemplate(Config{ConfigDir: dir, Path: DefaultFileName})
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "Informational announcements") ||
		strings.Contains(strings.ToLower(string(first)), "请假") {
		t.Fatalf("template leaked business content:\n%s", first)
	}
	if _, err := WriteTemplate(Config{ConfigDir: dir, Path: DefaultFileName}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("template overwrite existing private file")
	}
}
