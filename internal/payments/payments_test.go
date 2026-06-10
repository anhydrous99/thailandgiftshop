package payments

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

const testValidStripeCredentialsJSON = `{"secret_key":"sk_test_placeholder","webhook_signing_secret":"whsec_test_placeholder"}`

func TestNewProviderFromEnvironment(t *testing.T) {
	tests := []struct {
		name        string
		appEnv      string
		provider    string
		credentials string
		wantKind    string
		wantErr     error
		wantAnyErr  bool
	}{
		{
			name:        "explicit fake in production fails closed even with credentials",
			appEnv:      appenv.EnvironmentProduction,
			provider:    "fake",
			credentials: testValidStripeCredentialsJSON,
			wantErr:     ErrFakeProviderNotAllowedInProduction,
		},
		{
			name:     "explicit fake in development",
			appEnv:   "development",
			provider: "fake",
			wantKind: KindFake,
		},
		{
			name:     "explicit value is trimmed and case-insensitive",
			appEnv:   "development",
			provider: "  FAKE  ",
			wantKind: KindFake,
		},
		{
			name:        "explicit stripe with credentials",
			appEnv:      "development",
			provider:    "stripe",
			credentials: testValidStripeCredentialsJSON,
			wantKind:    KindStripe,
		},
		{
			name:     "explicit stripe without credentials",
			appEnv:   appenv.EnvironmentProduction,
			provider: "stripe",
			wantErr:  ErrStripeCredentialsNotConfigured,
		},
		{
			name:        "explicit stripe with malformed credentials JSON",
			appEnv:      "development",
			provider:    "stripe",
			credentials: "not-json",
			wantAnyErr:  true,
		},
		{
			name:        "explicit stripe with missing webhook signing secret",
			appEnv:      "development",
			provider:    "stripe",
			credentials: `{"secret_key":"sk_test_placeholder"}`,
			wantErr:     ErrStripeCredentialsNotConfigured,
		},
		{
			name:       "unsupported explicit provider value",
			appEnv:     "development",
			provider:   "paypal",
			wantAnyErr: true,
		},
		{
			name:        "unset provider with credentials selects stripe",
			appEnv:      appenv.EnvironmentProduction,
			credentials: testValidStripeCredentialsJSON,
			wantKind:    KindStripe,
		},
		{
			name:        "unset provider with whitespace credentials in production fails closed",
			appEnv:      appenv.EnvironmentProduction,
			credentials: "   ",
			wantErr:     ErrPaymentsProviderNotConfigured,
		},
		{
			name:     "unset provider without credentials in development selects fake",
			appEnv:   "development",
			wantKind: KindFake,
		},
		{
			name:     "unset provider without credentials with empty app env selects fake",
			appEnv:   "",
			wantKind: KindFake,
		},
		{
			name:    "unset provider without credentials in production fails closed",
			appEnv:  appenv.EnvironmentProduction,
			wantErr: ErrPaymentsProviderNotConfigured,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(appenv.EnvAppEnvironment, test.appEnv)
			t.Setenv(EnvPaymentsProvider, test.provider)
			t.Setenv(EnvStripeCredentialsSecretJSON, test.credentials)

			provider, err := NewProviderFromEnvironment(context.Background())
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("NewProviderFromEnvironment error = %v, want %v", err, test.wantErr)
				}
				if provider != nil {
					t.Fatalf("provider = %#v, want nil", provider)
				}
				return
			}
			if test.wantAnyErr {
				if err == nil {
					t.Fatalf("NewProviderFromEnvironment returned provider %T, want error", provider)
				}
				if provider != nil {
					t.Fatalf("provider = %#v, want nil", provider)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewProviderFromEnvironment returned error: %v", err)
			}
			if provider == nil {
				t.Fatal("provider = nil, want non-nil")
			}
			if provider.Kind() != test.wantKind {
				t.Fatalf("provider.Kind() = %q, want %q", provider.Kind(), test.wantKind)
			}
		})
	}
}

func TestNewProviderFromEnvironmentReturnsConcreteTypes(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	t.Setenv(EnvPaymentsProvider, "")
	t.Setenv(EnvStripeCredentialsSecretJSON, testValidStripeCredentialsJSON)

	provider, err := NewProviderFromEnvironment(context.Background())
	if err != nil {
		t.Fatalf("NewProviderFromEnvironment returned error: %v", err)
	}
	if _, ok := provider.(*StripeProvider); !ok {
		t.Fatalf("provider = %T, want *StripeProvider", provider)
	}

	t.Setenv(EnvStripeCredentialsSecretJSON, "")
	provider, err = NewProviderFromEnvironment(context.Background())
	if err != nil {
		t.Fatalf("NewProviderFromEnvironment returned error: %v", err)
	}
	if _, ok := provider.(*FakeProvider); !ok {
		t.Fatalf("provider = %T, want *FakeProvider", provider)
	}
}

func TestStripeCredentialsFromSecretManager(t *testing.T) {
	secretString := testValidStripeCredentialsJSON
	client := &fakeSecretsManagerClient{
		output: &secretsmanager.GetSecretValueOutput{SecretString: &secretString},
	}

	credentials, err := stripeCredentialsFromSecretManager(context.Background(), client, "thailandgiftshop/stripe/credentials")
	if err != nil {
		t.Fatalf("stripeCredentialsFromSecretManager returned error: %v", err)
	}
	if credentials.SecretKey != "sk_test_placeholder" || credentials.WebhookSigningSecret != "whsec_test_placeholder" {
		t.Fatalf("credentials = %#v, want parsed secret JSON", credentials)
	}
	if client.gotSecretID != "thailandgiftshop/stripe/credentials" {
		t.Fatalf("SecretId = %q, want stripe secret name", client.gotSecretID)
	}
}

func TestPublicBaseURLFromEnvironment(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "unset uses production default", value: "", want: DefaultPublicBaseURL},
		{name: "whitespace uses production default", value: "   ", want: DefaultPublicBaseURL},
		{name: "explicit value", value: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "trailing slash trimmed", value: "http://127.0.0.1:8080/", want: "http://127.0.0.1:8080"},
		{name: "surrounding whitespace trimmed", value: "  https://example.test  ", want: "https://example.test"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(EnvPublicBaseURL, test.value)
			if got := PublicBaseURLFromEnvironment(); got != test.want {
				t.Fatalf("PublicBaseURLFromEnvironment() = %q, want %q", got, test.want)
			}
		})
	}
}

type fakeSecretsManagerClient struct {
	output      *secretsmanager.GetSecretValueOutput
	err         error
	gotSecretID string
}

func (f *fakeSecretsManagerClient) GetSecretValue(ctx context.Context, input *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	_ = ctx
	_ = optFns

	if input == nil || input.SecretId == nil {
		return nil, fmt.Errorf("missing SecretId")
	}
	f.gotSecretID = *input.SecretId
	return f.output, f.err
}
