package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"golang.org/x/crypto/bcrypt"
)

// defaultSecretID mirrors the unexported adminCredentialsSecretName constant in
// infra/main.go:66 (that package is package main, so it can't be imported here).
const defaultSecretID = "thailandgiftshop/admin/credentials"

// defaultRegion matches the production region the rest of the repo pins to
// (cmd/seedcatalog/main.go:18). Production lives only in us-east-1.
const defaultRegion = "us-east-1"

// envAdminPassword is a non-argv way to supply the password (argv is visible in
// shell history and `ps`).
const envAdminPassword = "ADMIN_PASSWORD"

type adminSecret struct {
	PasswordHash  string `json:"password_hash"`
	SessionSecret string `json:"session_secret"`
}

// secretsManagerWriteAPI is the slice of the Secrets Manager client this tool
// needs, following the interface-only-what-you-use pattern in
// internal/payments/stripe.go:38-40 so the push path is unit-testable.
type secretsManagerWriteAPI interface {
	PutSecretValue(ctx context.Context, params *secretsmanager.PutSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	CreateSecret(ctx context.Context, params *secretsmanager.CreateSecretInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func generateAdminSecret(adminPassword string, cost int) (adminSecret, error) {
	sessionSecret, err := randomToken(48)
	if err != nil {
		return adminSecret{}, fmt.Errorf("generate session secret: %w", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), cost)
	if err != nil {
		return adminSecret{}, fmt.Errorf("bcrypt password: %w", err)
	}

	return adminSecret{
		PasswordHash:  string(hash),
		SessionSecret: sessionSecret,
	}, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// resolvePassword picks the password source: -password-stdin, then the
// ADMIN_PASSWORD env var, then the -password flag, otherwise a fresh random one.
// The bool reports whether the password was auto-generated.
func resolvePassword(passwordStdin bool, passwordFlag string) (string, bool, error) {
	if passwordStdin && strings.TrimSpace(passwordFlag) != "" {
		return "", false, errors.New("-password-stdin and -password are mutually exclusive")
	}

	if passwordStdin {
		password, err := readPasswordFromStdin()
		if err != nil {
			return "", false, err
		}
		if password == "" {
			return "", false, errors.New("-password-stdin was set but stdin was empty")
		}
		return password, false, nil
	}

	if envPassword := os.Getenv(envAdminPassword); strings.TrimSpace(envPassword) != "" {
		return envPassword, false, nil
	}

	if strings.TrimSpace(passwordFlag) != "" {
		return passwordFlag, false, nil
	}

	generated, err := randomToken(18)
	if err != nil {
		return "", false, fmt.Errorf("generate password: %w", err)
	}
	return generated, true, nil
}

func readPasswordFromStdin() (string, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	// Strip only a trailing newline so passwords containing spaces survive.
	return strings.TrimRight(string(data), "\r\n"), nil
}

func printPushPlan(secretID, region string) {
	fmt.Fprintf(os.Stderr, "Secret:  %s\n", secretID)
	fmt.Fprintf(os.Stderr, "Region:  %s\n", region)
	fmt.Fprintln(os.Stderr, "Action:  create or rotate (overwrites any existing value)")
	fmt.Fprintln(os.Stderr, "Note:    a fresh session secret is generated — this logs out all active admins and invalidates CSRF tokens.")
}

func pushAdminSecret(ctx context.Context, secretID, region, payload string, dryRun, assumeYes bool) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	return pushWithClient(ctx, secretsmanager.NewFromConfig(cfg), secretID, payload, dryRun, assumeYes)
}

func pushWithClient(ctx context.Context, client secretsManagerWriteAPI, secretID, payload string, dryRun, assumeYes bool) error {
	if dryRun {
		fmt.Fprintln(os.Stderr, "dry-run: no changes made")
		return nil
	}

	if !assumeYes {
		ok, err := confirm()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted")
		}
	}

	// Rotate the existing secret; if it does not exist yet (first-time
	// bootstrap), create it. One path covers both.
	_, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     &secretID,
		SecretString: &payload,
	})
	if err == nil {
		fmt.Fprintf(os.Stderr, "rotated secret %q\n", secretID)
		return nil
	}

	var notFound *smtypes.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("put secret %q: %w", secretID, err)
	}

	if _, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         &secretID,
		SecretString: &payload,
	}); err != nil {
		return fmt.Errorf("create secret %q: %w", secretID, err)
	}
	fmt.Fprintf(os.Stderr, "created secret %q\n", secretID)
	return nil
}

func confirm() (bool, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false, fmt.Errorf("stat stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		// stdin is a pipe/file (e.g. the password was piped in), so we cannot
		// prompt — make the operator opt in explicitly.
		return false, errors.New("stdin is not a terminal; re-run with -yes to confirm the write")
	}

	fmt.Fprint(os.Stderr, "Proceed? [y/N]: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func main() {
	outPath := flag.String("out", "", "path for the generated secret JSON (optional)")
	push := flag.Bool("push", false, "create or rotate the secret in AWS Secrets Manager")
	secretID := flag.String("secret-id", defaultSecretID, "Secrets Manager secret name/ARN to write")
	region := flag.String("region", envOrDefault("AWS_REGION", defaultRegion), "AWS region for Secrets Manager")
	password := flag.String("password", "", "admin password to hash (discouraged: visible in shell history); prefer -password-stdin or ADMIN_PASSWORD")
	passwordStdin := flag.Bool("password-stdin", false, "read the admin password from stdin")
	cost := flag.Int("cost", 12, "bcrypt cost")
	assumeYes := flag.Bool("yes", false, "skip the confirmation prompt when pushing")
	dryRun := flag.Bool("dry-run", false, "print the intended action without writing anything")
	flag.Parse()

	if *outPath == "" && !*push {
		fmt.Fprintln(os.Stderr, "at least one of -out / -push is required")
		os.Exit(2)
	}

	adminPassword, generatedPassword, err := resolvePassword(*passwordStdin, *password)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	secret, err := generateAdminSecret(adminPassword, *cost)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	payload, err := json.Marshal(secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode secret: %v\n", err)
		os.Exit(1)
	}

	if *push {
		printPushPlan(*secretID, *region)
		if err := pushAdminSecret(context.Background(), *secretID, *region, string(payload), *dryRun, *assumeYes); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	if *outPath != "" {
		if err := os.WriteFile(*outPath, payload, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "write secret JSON: %v\n", err)
			os.Exit(1)
		}
	}

	if generatedPassword {
		fmt.Printf("ADMIN_PASSWORD=%s\n", adminPassword)
	}
}
