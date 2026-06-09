package appenv

import (
	"os"
	"testing"
)

func TestIsProduction(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset", value: "", want: false},
		{name: "empty", value: "", want: false},
		{name: "whitespace", value: "   \t  ", want: false},
		{name: "production", value: "production", want: true},
		{name: "production title case", value: "Production", want: true},
		{name: "production upper case", value: "PRODUCTION", want: true},
		{name: "staging", value: "staging", want: false},
		{name: "development", value: "development", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "unset" {
				originalValue, hadValue := os.LookupEnv(EnvAppEnvironment)
				if err := os.Unsetenv(EnvAppEnvironment); err != nil {
					t.Fatalf("os.Unsetenv(%q) failed: %v", EnvAppEnvironment, err)
				}
				t.Cleanup(func() {
					if hadValue {
						_ = os.Setenv(EnvAppEnvironment, originalValue)
						return
					}
					_ = os.Unsetenv(EnvAppEnvironment)
				})
			} else {
				t.Setenv(EnvAppEnvironment, test.value)
			}
			if got := IsProduction(); got != test.want {
				t.Fatalf("IsProduction() = %v, want %v", got, test.want)
			}
		})
	}
}
