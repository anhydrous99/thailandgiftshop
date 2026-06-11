package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

const EnvAdminPasswordHash = "ADMIN_PASSWORD_HASH"
const EnvAdminSessionSecret = "ADMIN_SESSION_SECRET"
const EnvAdminCredentialsSecretJSON = "ADMIN_CREDENTIALS_SECRET_JSON"
const EnvAdminCredentialsSecretName = "ADMIN_CREDENTIALS_SECRET_NAME"
const EnvAdminOriginHeaderSecret = httpapi.EnvOriginHeaderSecret

type Credentials struct {
	PasswordHash  string
	SessionSecret string
}

type secretCredentials struct {
	PasswordHash  string `json:"password_hash"`
	SessionSecret string `json:"session_secret"`
}

type secretsManagerGetValueAPI interface {
	GetSecretValue(ctx context.Context, params *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

var ErrAdminCredentialsNotConfigured = errors.New("admin credentials not configured")

// CredentialsFromEnvironment resolves admin credentials, in order: the local
// ADMIN_PASSWORD_HASH/ADMIN_SESSION_SECRET pair (dev), an inline
// ADMIN_CREDENTIALS_SECRET_JSON blob (tests/local overrides), or the named
// Secrets Manager secret fetched at runtime (production). Loading the named
// secret at runtime — rather than baking it in via a deploy-time CloudFormation
// dynamic reference — lets a rotated password take effect without a redeploy.
func CredentialsFromEnvironment(ctx context.Context) (Credentials, error) {
	if credentials, ok := credentialsFromLocalEnvironment(); ok {
		return credentials, nil
	}

	if secretJSON := strings.TrimSpace(os.Getenv(EnvAdminCredentialsSecretJSON)); secretJSON != "" {
		return credentialsFromSecretString(secretJSON)
	}

	secretName := strings.TrimSpace(os.Getenv(EnvAdminCredentialsSecretName))
	if secretName == "" {
		return Credentials{}, ErrAdminCredentialsNotConfigured
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return Credentials{}, fmt.Errorf("load AWS config for admin credentials: %w", err)
	}

	return credentialsFromSecretManager(ctx, secretsmanager.NewFromConfig(cfg), secretName)
}

func credentialsFromSecretManager(ctx context.Context, client secretsManagerGetValueAPI, secretName string) (Credentials, error) {
	secretName = strings.TrimSpace(secretName)
	if secretName == "" {
		return Credentials{}, ErrAdminCredentialsNotConfigured
	}

	output, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &secretName})
	if err != nil {
		return Credentials{}, fmt.Errorf("get admin credentials secret %q: %w", secretName, err)
	}
	if output.SecretString == nil {
		return Credentials{}, ErrAdminCredentialsNotConfigured
	}

	return credentialsFromSecretString(*output.SecretString)
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
