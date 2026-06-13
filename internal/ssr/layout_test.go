package ssr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

const skipLinkStyleCSPHash = "sha256-rE+bFBvY9ntOvyDDnI4OBE9+BdhsJpyJJWxGWylO5oY="

func TestSkipLinkInlineStyleMatchesCSPHash(t *testing.T) {
	var buffer bytes.Buffer
	if err := siteHead(homeMetadata()).Render(context.Background(), &buffer); err != nil {
		t.Fatalf("render site head: %v", err)
	}

	_, afterOpen, ok := strings.Cut(buffer.String(), "<style>")
	if !ok {
		t.Fatal("rendered head missing inline style block")
	}
	style, _, ok := strings.Cut(afterOpen, "</style>")
	if !ok {
		t.Fatal("rendered head missing inline style close tag")
	}

	digest := sha256.Sum256([]byte(style))
	got := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
	if got != skipLinkStyleCSPHash {
		t.Fatalf("skip-link inline style hash = %q, want %q; update CloudFront CSP if this intentional", got, skipLinkStyleCSPHash)
	}
}
