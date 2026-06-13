package ssr

import (
	"strings"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
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
