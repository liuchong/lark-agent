package lark

import (
	"context"
	"os"
	"strings"
)

type CredentialRefs struct {
	Service             string
	AppSecretAccount    string
	UserTokenAccount    string
	RefreshTokenAccount string
}

type Credentials struct {
	AppSecret       string
	UserAccessToken string
	RefreshToken    string
}

func LoadCredentials(ctx context.Context, refs CredentialRefs) (Credentials, error) {
	appSecret, err := readSecret(ctx, refs.Service, refs.AppSecretAccount, "LARK_AGENT_APP_SECRET")
	if err != nil {
		return Credentials{}, err
	}
	userToken := ""
	if strings.TrimSpace(refs.UserTokenAccount) != "" {
		userToken, _ = readSecret(ctx, refs.Service, refs.UserTokenAccount, "LARK_AGENT_USER_ACCESS_TOKEN")
	}
	refreshToken := ""
	if strings.TrimSpace(refs.RefreshTokenAccount) != "" {
		refreshToken, _ = readSecret(ctx, refs.Service, refs.RefreshTokenAccount, "LARK_AGENT_REFRESH_TOKEN")
	}
	return Credentials{AppSecret: appSecret, UserAccessToken: userToken, RefreshToken: refreshToken}, nil
}

func StoreCredentials(ctx context.Context, refs CredentialRefs, credentials Credentials) error {
	if err := writeSecret(ctx, refs.Service, refs.AppSecretAccount, credentials.AppSecret); err != nil {
		return err
	}
	if strings.TrimSpace(credentials.UserAccessToken) != "" && strings.TrimSpace(refs.UserTokenAccount) != "" {
		if err := writeSecret(ctx, refs.Service, refs.UserTokenAccount, credentials.UserAccessToken); err != nil {
			return err
		}
	}
	if strings.TrimSpace(credentials.RefreshToken) != "" && strings.TrimSpace(refs.RefreshTokenAccount) != "" {
		if err := writeSecret(ctx, refs.Service, refs.RefreshTokenAccount, credentials.RefreshToken); err != nil {
			return err
		}
	}
	return nil
}

func DeleteCredentials(ctx context.Context, refs CredentialRefs) error {
	for _, account := range []string{refs.AppSecretAccount, refs.UserTokenAccount, refs.RefreshTokenAccount} {
		if strings.TrimSpace(account) == "" {
			continue
		}
		if err := deleteSecret(ctx, refs.Service, account); err != nil {
			return err
		}
	}
	return nil
}

func envSecret(name string) string {
	return os.Getenv(name)
}
