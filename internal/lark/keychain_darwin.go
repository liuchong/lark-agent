//go:build darwin

package lark

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	errs "github.com/liuchong/lark-agent/internal/apperr"
)

func readSecret(ctx context.Context, service, account, envName string) (string, error) {
	if value := strings.TrimSpace(envSecret(envName)); value != "" {
		return value, nil
	}
	if strings.TrimSpace(service) == "" || strings.TrimSpace(account) == "" {
		return "", errs.NewConfigError(errs.SubtypeNotConfigured, "keychain reference is not configured")
	}
	command := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-w", "-s", service, "-a", account)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", errs.NewConfigError(errs.SubtypeNotConfigured, "read lark credential from Keychain").WithCause(err)
	}
	value := strings.TrimSpace(stdout.String())
	if value == "" {
		return "", errs.NewConfigError(errs.SubtypeNotConfigured, "keychain credential is empty")
	}
	return value, nil
}

func writeSecret(ctx context.Context, service, account, value string) error {
	if strings.TrimSpace(service) == "" || strings.TrimSpace(account) == "" {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "keychain reference is not configured")
	}
	if strings.TrimSpace(value) == "" {
		return errs.NewValidationError(errs.SubtypeInvalidArgument, "credential value is empty")
	}
	command := exec.CommandContext(ctx, "/usr/bin/security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", value)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "write lark credential to Keychain").WithCause(err)
	}
	return nil
}

func deleteSecret(ctx context.Context, service, account string) error {
	if strings.TrimSpace(service) == "" || strings.TrimSpace(account) == "" {
		return errs.NewConfigError(errs.SubtypeNotConfigured, "keychain reference is not configured")
	}
	command := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", service, "-a", account)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if strings.Contains(stderr.String(), "could not be found") {
			return nil
		}
		return errs.NewConfigError(errs.SubtypeNotConfigured, "delete lark credential from Keychain").WithCause(err)
	}
	return nil
}
