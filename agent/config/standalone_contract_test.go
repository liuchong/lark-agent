package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsAreIndependentFromOfficialCLIConfig(t *testing.T) {
	home := t.TempDir()
	paths := DefaultPaths(home)
	if paths.ConfigDir != filepath.Join(home, ".config", "lark-agent") {
		t.Fatalf("config dir=%q", paths.ConfigDir)
	}
	if paths.StateDir != filepath.Join(home, "Library", "Application Support", "lark-agent") {
		t.Fatalf("state dir=%q", paths.StateDir)
	}
	if paths.LogDir != filepath.Join(home, "Library", "Logs", "lark-agent") {
		t.Fatalf("log dir=%q", paths.LogDir)
	}
}

func TestDefaultConfigUsesSDKKeychainBoundary(t *testing.T) {
	cfg := Default()
	if cfg.Lark.KeychainService != "lark-agent" ||
		cfg.Lark.AppSecretKeychainKey == "" ||
		cfg.Lark.UserTokenKeychainKey == "" ||
		cfg.Lark.RefreshTokenKeychainKey == "" {
		t.Fatalf("keychain refs=%+v", cfg.Lark)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("default config without app_id and owner must not validate")
	}
	cfg.Lark.AppID = "cli_test"
	cfg.Owner.OpenID = "ou_owner"
	cfg.Owner.Name = "测试负责人"
	cfg.Workspace.Root = t.TempDir()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
