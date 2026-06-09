package appenv

import (
	"os"
	"strings"
)

const EnvAppEnvironment = "APP_ENV"
const EnvironmentProduction = "production"

func IsProduction() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvAppEnvironment)), EnvironmentProduction)
}
