package main

import (
	"testing"

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
