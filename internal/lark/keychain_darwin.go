//go:build darwin

package lark

import (
	"context"

	"github.com/liuchong/lark-agent/internal/secretstore"
)

func readSecret(ctx context.Context, service, account, envName string) (string, error) {
	return secretstore.Read(ctx, service, account, envName)
}

func writeSecret(ctx context.Context, service, account, value string) error {
	return secretstore.Write(ctx, service, account, value)
}

func deleteSecret(ctx context.Context, service, account string) error {
	return secretstore.Delete(ctx, service, account)
}
