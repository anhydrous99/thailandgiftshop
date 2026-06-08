package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
)

type adminSecret struct {
	PasswordHash  string `json:"password_hash"`
	SessionSecret string `json:"session_secret"`
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

func main() {
	outPath := flag.String("out", "", "path for the generated secret JSON")
	password := flag.String("password", "", "admin password to hash; generated when omitted")
	cost := flag.Int("cost", 12, "bcrypt cost")
	flag.Parse()

	if *outPath == "" {
		fmt.Fprintln(os.Stderr, "-out is required")
		os.Exit(2)
	}

	adminPassword := *password
	generatedPassword := false
	if adminPassword == "" {
		var err error
		adminPassword, err = randomToken(18)
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate password: %v\n", err)
			os.Exit(1)
		}
		generatedPassword = true
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

	if err := os.WriteFile(*outPath, payload, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "write secret JSON: %v\n", err)
		os.Exit(1)
	}

	if generatedPassword {
		fmt.Printf("ADMIN_PASSWORD=%s\n", adminPassword)
	}
}
