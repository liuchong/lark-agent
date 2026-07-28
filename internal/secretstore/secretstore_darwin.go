//go:build darwin

package secretstore

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

func readPlatform(ctx context.Context, service, account string) (string, error) {
	command := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-w", "-s", service, "-a", account)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		return "", errs.NewConfigError(errs.SubtypeNotConfigured, "read credential from Keychain").WithCause(err)
	}
	value := strings.TrimSpace(stdout.String())
	if value == "" {
		return "", errs.NewConfigError(errs.SubtypeNotConfigured, "keychain credential is empty")
	}
	return value, nil
}

func writePlatform(ctx context.Context, service, account, value string) error {
	command := exec.CommandContext(ctx, "/usr/bin/security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", value)
	if err := command.Run(); err != nil {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "write credential to Keychain").WithCause(err)
	}
	return nil
}

func deletePlatform(ctx context.Context, service, account string) error {
	command := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", service, "-a", account)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if strings.Contains(stderr.String(), "could not be found") {
			return nil
		}
		return errs.NewConfigError(errs.SubtypeNotConfigured, "delete credential from Keychain").WithCause(err)
	}
	return nil
}
