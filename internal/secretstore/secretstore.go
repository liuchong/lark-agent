// Package secretstore owns platform credential storage for non-secret
// configuration references.
package secretstore

import (
	"context"
	"os"
	"strings"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

func Read(ctx context.Context, service, account, envName string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value, nil
	}
	if strings.TrimSpace(service) == "" || strings.TrimSpace(account) == "" {
		return "", errs.NewConfigError(errs.SubtypeNotConfigured, "credential reference is not configured")
	}
	return readPlatform(ctx, service, account)
}

func Write(ctx context.Context, service, account, value string) error {
	if strings.TrimSpace(service) == "" || strings.TrimSpace(account) == "" {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "credential reference is not configured")
	}
	if strings.TrimSpace(value) == "" {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "credential value is empty")
	}
	return writePlatform(ctx, service, account, value)
}

func Delete(ctx context.Context, service, account string) error {
	if strings.TrimSpace(service) == "" || strings.TrimSpace(account) == "" {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "credential reference is not configured")
	}
	return deletePlatform(ctx, service, account)
}
