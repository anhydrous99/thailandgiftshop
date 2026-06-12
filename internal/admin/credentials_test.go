package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"golang.org/x/crypto/bcrypt"
)

type fakeSecretsManager struct {
	output *secretsmanager.GetSecretValueOutput
	err    error
	calls  []string
}

func (f *fakeSecretsManager) GetSecretValue(_ context.Context, params *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if params.SecretId != nil {
		f.calls = append(f.calls, *params.SecretId)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.output, nil
}

func secretStringPtr(s string) *string { return &s }

func TestCredentialsFromSecretManagerParsesSecret(t *testing.T) {
	const hash = "$2a$10$abcdefghijklmnopqrstuvwx"
	secretJSON := `{"password_hash":"` + hash + `","session_secret":"sess-secret"}`
	fake := &fakeSecretsManager{output: &secretsmanager.GetSecretValueOutput{SecretString: secretStringPtr(secretJSON)}}

	creds, err := credentialsFromSecretManager(context.Background(), fake, "thailandgiftshop/admin/credentials")
	if err != nil {
		t.Fatalf("credentialsFromSecretManager: %v", err)
	}
	if creds.PasswordHash != hash {
		t.Fatalf("PasswordHash = %q, want %q", creds.PasswordHash, hash)
	}
	if creds.SessionSecret != "sess-secret" {
		t.Fatalf("SessionSecret = %q, want %q", creds.SessionSecret, "sess-secret")
	}
	if len(fake.calls) != 1 || fake.calls[0] != "thailandgiftshop/admin/credentials" {
		t.Fatalf("GetSecretValue calls = %#v, want one call for the secret id", fake.calls)
	}
}

func TestCredentialsFromSecretManagerPropagatesError(t *testing.T) {
	fake := &fakeSecretsManager{err: errors.New("boom")}
	if _, err := credentialsFromSecretManager(context.Background(), fake, "sid"); err == nil {
		t.Fatal("expected an error when GetSecretValue fails")
	}
}

func TestCredentialsFromSecretManagerRejectsEmptySecretString(t *testing.T) {
	fake := &fakeSecretsManager{output: &secretsmanager.GetSecretValueOutput{SecretString: nil}}
	_, err := credentialsFromSecretManager(context.Background(), fake, "sid")
	if !errors.Is(err, ErrAdminCredentialsNotConfigured) {
		t.Fatalf("err = %v, want ErrAdminCredentialsNotConfigured", err)
	}
}

func TestCredentialsFromEnvironmentPrefersLocalPair(t *testing.T) {
	t.Setenv(EnvAdminPasswordHash, "local-hash")
	t.Setenv(EnvAdminSessionSecret, "local-session")
	t.Setenv(EnvAdminCredentialsSecretJSON, `{"password_hash":"inline","session_secret":"inline"}`)
	t.Setenv(EnvAdminCredentialsSecretName, "thailandgiftshop/admin/credentials")

	creds, err := CredentialsFromEnvironment(context.Background())
	if err != nil {
		t.Fatalf("CredentialsFromEnvironment: %v", err)
	}
	if creds.PasswordHash != "local-hash" || creds.SessionSecret != "local-session" {
		t.Fatalf("creds = %+v, want the local ADMIN_PASSWORD_HASH/ADMIN_SESSION_SECRET pair", creds)
	}
}

func TestCredentialsFromEnvironmentUsesInlineJSONBeforeSecretName(t *testing.T) {
	t.Setenv(EnvAdminPasswordHash, "")
	t.Setenv(EnvAdminSessionSecret, "")
	t.Setenv(EnvAdminCredentialsSecretJSON, `{"password_hash":"inline-hash","session_secret":"inline-session"}`)
	// A secret name is set too, but the inline JSON must win without any AWS call.
	t.Setenv(EnvAdminCredentialsSecretName, "thailandgiftshop/admin/credentials")

	creds, err := CredentialsFromEnvironment(context.Background())
	if err != nil {
		t.Fatalf("CredentialsFromEnvironment: %v", err)
	}
	if creds.PasswordHash != "inline-hash" || creds.SessionSecret != "inline-session" {
		t.Fatalf("creds = %+v, want the inline ADMIN_CREDENTIALS_SECRET_JSON value", creds)
	}
}

func TestCredentialsFromEnvironmentNotConfigured(t *testing.T) {
	t.Setenv(EnvAdminPasswordHash, "")
	t.Setenv(EnvAdminSessionSecret, "")
	t.Setenv(EnvAdminCredentialsSecretJSON, "")
	t.Setenv(EnvAdminCredentialsSecretName, "")

	if _, err := CredentialsFromEnvironment(context.Background()); !errors.Is(err, ErrAdminCredentialsNotConfigured) {
		t.Fatalf("err = %v, want ErrAdminCredentialsNotConfigured", err)
	}
}

// TestCredentialsForRequestRefreshesAfterTTL is the regression guard for the
// rotation bug: a warm handler must pick up a rotated secret once the cache TTL
// elapses, without a cold start.
func TestCredentialsForRequestRefreshesAfterTTL(t *testing.T) {
	current := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	h := &Handler{now: func() time.Time { return current }}

	t.Setenv(EnvAdminPasswordHash, "")
	t.Setenv(EnvAdminSessionSecret, "")
	t.Setenv(EnvAdminCredentialsSecretJSON, `{"password_hash":"hash-1","session_secret":"sess-1"}`)

	if got := h.credentialsForRequest(context.Background()); got.PasswordHash != "hash-1" {
		t.Fatalf("initial PasswordHash = %q, want hash-1", got.PasswordHash)
	}

	// Rotate the source. Within the TTL the cached value is still served.
	t.Setenv(EnvAdminCredentialsSecretJSON, `{"password_hash":"hash-2","session_secret":"sess-2"}`)
	current = current.Add(adminCredentialsTTL - time.Second)
	if got := h.credentialsForRequest(context.Background()); got.PasswordHash != "hash-1" {
		t.Fatalf("within TTL PasswordHash = %q, want cached hash-1", got.PasswordHash)
	}

	// Once the TTL elapses the rotated value is reloaded.
	current = current.Add(2 * time.Second)
	if got := h.credentialsForRequest(context.Background()); got.PasswordHash != "hash-2" {
		t.Fatalf("after TTL PasswordHash = %q, want rotated hash-2", got.PasswordHash)
	}
}

func TestValidPasswordDoesNotExtendCredentialCacheTTL(t *testing.T) {
	current := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	hash, err := bcrypt.GenerateFromPassword([]byte("rotating-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword returned error: %v", err)
	}
	h := &Handler{now: func() time.Time { return current }}

	t.Setenv(EnvAdminPasswordHash, "")
	t.Setenv(EnvAdminSessionSecret, "")
	t.Setenv(EnvAdminCredentialsSecretJSON, `{"password_hash":"`+string(hash)+`","session_secret":"sess-1"}`)
	credentials := h.credentialsForRequest(context.Background())

	current = current.Add(adminCredentialsTTL - time.Second)
	if !h.validPasswordWithCredentials(credentials, "rotating-password") {
		t.Fatal("validPasswordWithCredentials rejected the cached password")
	}

	t.Setenv(EnvAdminCredentialsSecretJSON, `{"password_hash":"rotated-hash","session_secret":"sess-2"}`)
	current = current.Add(2 * time.Second)
	if got := h.credentialsForRequest(context.Background()); got.SessionSecret != "sess-2" {
		t.Fatalf("after original TTL SessionSecret = %q, want rotated sess-2", got.SessionSecret)
	}
}

// TestCredentialsForRequestKeepsCacheOnLoadError confirms a transient load
// failure after expiry does not lock admins out — the last-known-good
// credentials keep being served.
func TestCredentialsForRequestKeepsCacheOnLoadError(t *testing.T) {
	current := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	h := &Handler{now: func() time.Time { return current }}

	t.Setenv(EnvAdminPasswordHash, "")
	t.Setenv(EnvAdminSessionSecret, "")
	t.Setenv(EnvAdminCredentialsSecretJSON, `{"password_hash":"hash-1","session_secret":"sess-1"}`)
	if got := h.credentialsForRequest(context.Background()); got.PasswordHash != "hash-1" {
		t.Fatalf("initial PasswordHash = %q, want hash-1", got.PasswordHash)
	}

	// Source goes away (nothing configured) and the TTL elapses.
	t.Setenv(EnvAdminCredentialsSecretJSON, "")
	current = current.Add(adminCredentialsTTL + time.Second)
	if got := h.credentialsForRequest(context.Background()); got.PasswordHash != "hash-1" {
		t.Fatalf("after load failure PasswordHash = %q, want retained hash-1", got.PasswordHash)
	}
}
