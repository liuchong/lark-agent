//go:build !darwin

package lark

import (
	"context"
	"strings"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

func readSecret(_ context.Context, _, _, envName string) (string, error) {
	if value := strings.TrimSpace(envSecret(envName)); value != "" {
		return value, nil
	}
	return "", errs.NewConfigError(errs.SubtypeNotConfigured, "lark credential is not configured")
}

func writeSecret(_ context.Context, _, _, _ string) error {
	return errs.NewConfigError(errs.SubtypeNotConfigured, "Keychain credential storage requires macOS")
}

func deleteSecret(_ context.Context, _, _ string) error {
	return errs.NewConfigError(errs.SubtypeNotConfigured, "Keychain credential storage requires macOS")
}
