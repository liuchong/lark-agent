//go:build !darwin

package secretstore

import (
	"context"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

func readPlatform(_ context.Context, _, _ string) (string, error) {
	return "", errs.NewConfigError(errs.SubtypeNotConfigured, "credential is not configured")
}

func writePlatform(_ context.Context, _, _, _ string) error {
	return errs.NewConfigError(errs.SubtypeNotConfigured, "Keychain credential storage requires macOS")
}

func deletePlatform(_ context.Context, _, _ string) error {
	return errs.NewConfigError(errs.SubtypeNotConfigured, "Keychain credential storage requires macOS")
}
