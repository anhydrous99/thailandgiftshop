package admin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

const EnvAdminPasswordHash = "ADMIN_PASSWORD_HASH"
const EnvAdminSessionSecret = "ADMIN_SESSION_SECRET"
const EnvAdminCredentialsSecretJSON = "ADMIN_CREDENTIALS_SECRET_JSON"

type Credentials struct {
	PasswordHash  string
	SessionSecret string
}

type secretCredentials struct {
	PasswordHash  string `json:"password_hash"`
	SessionSecret string `json:"session_secret"`
}

var ErrAdminCredentialsNotConfigured = errors.New("admin credentials not configured")

func CredentialsFromEnvironment(ctx context.Context) (Credentials, error) {
	_ = ctx

	if credentials, ok := credentialsFromLocalEnvironment(); ok {
		return credentials, nil
	}

	secretJSON := strings.TrimSpace(os.Getenv(EnvAdminCredentialsSecretJSON))
	if secretJSON == "" {
		return Credentials{}, ErrAdminCredentialsNotConfigured
	}

	return credentialsFromSecretString(secretJSON)
}

func credentialsFromLocalEnvironment() (Credentials, bool) {
	passwordHash := strings.TrimSpace(os.Getenv(EnvAdminPasswordHash))
	sessionSecret := strings.TrimSpace(os.Getenv(EnvAdminSessionSecret))
	if passwordHash == "" || sessionSecret == "" {
		return Credentials{}, false
	}

	return Credentials{PasswordHash: passwordHash, SessionSecret: sessionSecret}, true
}

func credentialsFromSecretString(secretString string) (Credentials, error) {
	var parsed secretCredentials
	if err := json.Unmarshal([]byte(secretString), &parsed); err != nil {
		return Credentials{}, err
	}
	passwordHash := strings.TrimSpace(parsed.PasswordHash)
	sessionSecret := strings.TrimSpace(parsed.SessionSecret)
	if passwordHash == "" || sessionSecret == "" {
		return Credentials{}, ErrAdminCredentialsNotConfigured
	}

	return Credentials{PasswordHash: passwordHash, SessionSecret: sessionSecret}, nil
}
