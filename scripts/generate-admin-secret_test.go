package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"golang.org/x/crypto/bcrypt"
)

func TestGenerateAdminSecretHashesPassword(t *testing.T) {
	const password = "correct horse battery staple"

	secret, err := generateAdminSecret(password, bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generateAdminSecret: %v", err)
	}

	if secret.PasswordHash == "" {
		t.Fatal("PasswordHash is empty")
	}
	if secret.SessionSecret == "" {
		t.Fatal("SessionSecret is empty")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(secret.PasswordHash), []byte(password)); err != nil {
		t.Fatalf("PasswordHash does not match password: %v", err)
	}
}

func TestGenerateAdminSecretFreshSessionEachCall(t *testing.T) {
	first, err := generateAdminSecret("pw", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generateAdminSecret: %v", err)
	}
	second, err := generateAdminSecret("pw", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generateAdminSecret: %v", err)
	}
	if first.SessionSecret == second.SessionSecret {
		t.Fatal("expected a distinct session secret on each call")
	}
}

type fakeSecretsManager struct {
	putErr      error
	createErr   error
	putCalls    []*secretsmanager.PutSecretValueInput
	createCalls []*secretsmanager.CreateSecretInput
}

func (f *fakeSecretsManager) PutSecretValue(_ context.Context, params *secretsmanager.PutSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	f.putCalls = append(f.putCalls, params)
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &secretsmanager.PutSecretValueOutput{}, nil
}

func (f *fakeSecretsManager) CreateSecret(_ context.Context, params *secretsmanager.CreateSecretInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	f.createCalls = append(f.createCalls, params)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &secretsmanager.CreateSecretOutput{}, nil
}

func mustPayload(t *testing.T, password string) string {
	t.Helper()
	secret, err := generateAdminSecret(password, bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generateAdminSecret: %v", err)
	}
	payload, err := json.Marshal(secret)
	if err != nil {
		t.Fatalf("marshal secret: %v", err)
	}
	return string(payload)
}

func assertValidPayload(t *testing.T, payload, password string) {
	t.Helper()
	var got adminSecret
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("unmarshal written secret: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(password)); err != nil {
		t.Fatalf("written hash does not match password: %v", err)
	}
	if got.SessionSecret == "" {
		t.Fatal("written session secret is empty")
	}
}

func TestPushWithClientRotatesExistingSecret(t *testing.T) {
	const password = "rotate-me"
	payload := mustPayload(t, password)
	fake := &fakeSecretsManager{}

	if err := pushWithClient(context.Background(), fake, "sid", payload, false, true); err != nil {
		t.Fatalf("pushWithClient: %v", err)
	}

	if len(fake.putCalls) != 1 {
		t.Fatalf("want 1 PutSecretValue call, got %d", len(fake.putCalls))
	}
	if len(fake.createCalls) != 0 {
		t.Fatalf("want 0 CreateSecret calls, got %d", len(fake.createCalls))
	}
	if got := *fake.putCalls[0].SecretId; got != "sid" {
		t.Fatalf("SecretId = %q, want %q", got, "sid")
	}
	assertValidPayload(t, *fake.putCalls[0].SecretString, password)
}

func TestPushWithClientCreatesMissingSecret(t *testing.T) {
	const password = "create-me"
	payload := mustPayload(t, password)
	fake := &fakeSecretsManager{putErr: &smtypes.ResourceNotFoundException{}}

	if err := pushWithClient(context.Background(), fake, "sid", payload, false, true); err != nil {
		t.Fatalf("pushWithClient: %v", err)
	}

	if len(fake.putCalls) != 1 {
		t.Fatalf("want 1 PutSecretValue attempt, got %d", len(fake.putCalls))
	}
	if len(fake.createCalls) != 1 {
		t.Fatalf("want 1 CreateSecret call, got %d", len(fake.createCalls))
	}
	if got := *fake.createCalls[0].Name; got != "sid" {
		t.Fatalf("CreateSecret Name = %q, want %q", got, "sid")
	}
	assertValidPayload(t, *fake.createCalls[0].SecretString, password)
}

func TestPushWithClientPropagatesPutError(t *testing.T) {
	payload := mustPayload(t, "pw")
	fake := &fakeSecretsManager{putErr: errors.New("boom")}

	err := pushWithClient(context.Background(), fake, "sid", payload, false, true)
	if err == nil {
		t.Fatal("expected an error from a non-not-found Put failure")
	}
	if len(fake.createCalls) != 0 {
		t.Fatalf("CreateSecret must not be called on a generic Put error, got %d calls", len(fake.createCalls))
	}
}

func TestPushWithClientDryRunMakesNoCalls(t *testing.T) {
	payload := mustPayload(t, "pw")
	fake := &fakeSecretsManager{}

	if err := pushWithClient(context.Background(), fake, "sid", payload, true, true); err != nil {
		t.Fatalf("pushWithClient dry-run: %v", err)
	}
	if len(fake.putCalls)+len(fake.createCalls) != 0 {
		t.Fatalf("dry-run made AWS calls: %d put, %d create", len(fake.putCalls), len(fake.createCalls))
	}
}

func TestResolvePasswordStdinFlagConflict(t *testing.T) {
	if _, _, err := resolvePassword(true, "from-flag"); err == nil {
		t.Fatal("expected an error when -password-stdin and -password are both set")
	}
}

func TestResolvePasswordEnvOverridesFlag(t *testing.T) {
	t.Setenv(envAdminPassword, "from-env")

	password, generated, err := resolvePassword(false, "from-flag")
	if err != nil {
		t.Fatalf("resolvePassword: %v", err)
	}
	if generated {
		t.Fatal("password should not be reported as generated")
	}
	if password != "from-env" {
		t.Fatalf("password = %q, want %q", password, "from-env")
	}
}

func TestResolvePasswordUsesFlag(t *testing.T) {
	t.Setenv(envAdminPassword, "")

	password, generated, err := resolvePassword(false, "from-flag")
	if err != nil {
		t.Fatalf("resolvePassword: %v", err)
	}
	if generated {
		t.Fatal("password should not be reported as generated")
	}
	if password != "from-flag" {
		t.Fatalf("password = %q, want %q", password, "from-flag")
	}
}

func TestResolvePasswordGeneratesWhenAbsent(t *testing.T) {
	t.Setenv(envAdminPassword, "")

	password, generated, err := resolvePassword(false, "")
	if err != nil {
		t.Fatalf("resolvePassword: %v", err)
	}
	if !generated {
		t.Fatal("password should be reported as generated")
	}
	if password == "" {
		t.Fatal("generated password is empty")
	}
}

func TestResolvePasswordReadsStdin(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "pw")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := tmp.WriteString("super secret\n"); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		t.Fatalf("seek temp: %v", err)
	}

	orig := os.Stdin
	os.Stdin = tmp
	t.Cleanup(func() { os.Stdin = orig })

	password, generated, err := resolvePassword(true, "")
	if err != nil {
		t.Fatalf("resolvePassword: %v", err)
	}
	if generated {
		t.Fatal("password should not be reported as generated")
	}
	if password != "super secret" {
		t.Fatalf("password = %q, want %q (trailing newline stripped, inner space kept)", password, "super secret")
	}
}
