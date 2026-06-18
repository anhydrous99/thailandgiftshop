package ssr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
)

func TestValidateProductionSigningSecretsAllowsNonProductionUnset(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	t.Setenv(cart.EnvCookieSecret, "")
	t.Setenv(commerce.EnvSessionSecret, "")

	if err := validateProductionSigningSecrets(); err != nil {
		t.Fatalf("validateProductionSigningSecrets returned error: %v", err)
	}
}

func TestValidateProductionSigningSecretsRejectsMissingOrShortProductionSecrets(t *testing.T) {
	valid := strings.Repeat("a", minimumProductionSigningSecretLength)
	tests := []struct {
		name       string
		cartSecret string
		session    string
		wantName   string
	}{
		{name: "missing cart secret", cartSecret: "", session: valid, wantName: cart.EnvCookieSecret},
		{name: "short cart secret", cartSecret: "too-short", session: valid, wantName: cart.EnvCookieSecret},
		{name: "missing customer session secret", cartSecret: valid, session: "", wantName: commerce.EnvSessionSecret},
		{name: "short customer session secret", cartSecret: valid, session: "too-short", wantName: commerce.EnvSessionSecret},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
			t.Setenv(cart.EnvCookieSecret, test.cartSecret)
			t.Setenv(commerce.EnvSessionSecret, test.session)

			err := validateProductionSigningSecrets()
			if err == nil {
				t.Fatal("validateProductionSigningSecrets returned nil, want error")
			}
			if !strings.Contains(err.Error(), test.wantName) {
				t.Fatalf("error = %q, want env var %q", err.Error(), test.wantName)
			}
		})
	}
}

func TestValidateProductionSigningSecretsAcceptsConfiguredProductionSecrets(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
	t.Setenv(cart.EnvCookieSecret, strings.Repeat("c", minimumProductionSigningSecretLength))
	t.Setenv(commerce.EnvSessionSecret, strings.Repeat("s", minimumProductionSigningSecretLength))

	if err := validateProductionSigningSecrets(); err != nil {
		t.Fatalf("validateProductionSigningSecrets returned error: %v", err)
	}
}

func TestConfigureCheckoutFromEnvironmentFailsClosedInProduction(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
	t.Setenv(payments.EnvPaymentsProvider, "")
	t.Setenv(payments.EnvStripeCredentialsSecretJSON, "")
	t.Setenv(payments.EnvStripeCredentialsSecretName, "")

	handler := NewHandler(nil)
	err := handler.configureCheckoutFromEnvironment(context.Background(), commerce.NewMemoryStore(), email.NewFakeSender(), nil, nil)
	if !errors.Is(err, payments.ErrPaymentsProviderNotConfigured) {
		t.Fatalf("configureCheckoutFromEnvironment error = %v, want %v", err, payments.ErrPaymentsProviderNotConfigured)
	}
	if handler.payments != nil || handler.checkout != nil {
		t.Fatalf("handler payments = %#v checkout = %#v, want neither configured after production startup failure", handler.payments, handler.checkout)
	}
}

func TestConfigureCheckoutFromEnvironmentAllowsUnavailableProviderOutsideProduction(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	t.Setenv(payments.EnvPaymentsProvider, payments.KindStripe)
	t.Setenv(payments.EnvStripeCredentialsSecretJSON, "")
	t.Setenv(payments.EnvStripeCredentialsSecretName, "")

	handler := NewHandler(nil)
	if err := handler.configureCheckoutFromEnvironment(context.Background(), commerce.NewMemoryStore(), email.NewFakeSender(), nil, nil); err != nil {
		t.Fatalf("configureCheckoutFromEnvironment returned error outside production: %v", err)
	}
	if handler.payments != nil || handler.checkout != nil {
		t.Fatalf("handler payments = %#v checkout = %#v, want checkout disabled outside production when provider is unavailable", handler.payments, handler.checkout)
	}
}
