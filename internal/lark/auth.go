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

type UserTokens struct {
	AccessToken  string
	RefreshToken string
}

type UserTokenStore interface {
	LoadUserTokens(context.Context) (UserTokens, error)
	StoreUserTokens(context.Context, UserTokens) error
}

type KeychainUserTokenStore struct {
	refs CredentialRefs
}

func NewKeychainUserTokenStore(refs CredentialRefs) *KeychainUserTokenStore {
	return &KeychainUserTokenStore{refs: refs}
}

func (s *KeychainUserTokenStore) LoadUserTokens(ctx context.Context) (UserTokens, error) {
	if s == nil {
		return UserTokens{}, nil
	}
	accessToken, _ := readSecret(ctx, s.refs.Service, s.refs.UserTokenAccount, "LARK_AGENT_USER_ACCESS_TOKEN")
	refreshToken, _ := readSecret(ctx, s.refs.Service, s.refs.RefreshTokenAccount, "LARK_AGENT_REFRESH_TOKEN")
	return UserTokens{AccessToken: accessToken, RefreshToken: refreshToken}, nil
}

func (s *KeychainUserTokenStore) StoreUserTokens(ctx context.Context, tokens UserTokens) error {
	if s == nil {
		return nil
	}
	if err := writeSecret(ctx, s.refs.Service, s.refs.RefreshTokenAccount, tokens.RefreshToken); err != nil {
		return err
	}
	return writeSecret(ctx, s.refs.Service, s.refs.UserTokenAccount, tokens.AccessToken)
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
