package httpapi

import (
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestHTMLResponseWithCookiesMergesHeaders(t *testing.T) {
	response := HTMLResponseWithCookies(http.StatusOK, "body", map[string]string{"Cache-Control": "no-store"}, []string{"a=1"})
	if response.StatusCode != http.StatusOK || response.Body != "body" {
		t.Fatalf("response = %#v", response)
	}
	if got := response.Headers["Content-Type"]; got != HTMLContentType {
		t.Fatalf("Content-Type = %q, want %q", got, HTMLContentType)
	}
	if got := response.Headers["Cache-Control"]; got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if len(response.Cookies) != 1 || response.Cookies[0] != "a=1" {
		t.Fatalf("Cookies = %v", response.Cookies)
	}
}

func TestTypedResponseContentTypes(t *testing.T) {
	if got := TextResponse(http.StatusOK, "", nil).Headers["Content-Type"]; got != TextContentType {
		t.Fatalf("text Content-Type = %q", got)
	}
	if got := XMLResponse(http.StatusOK, "", nil).Headers["Content-Type"]; got != XMLContentType {
		t.Fatalf("xml Content-Type = %q", got)
	}
	if got := HTMLResponse(http.StatusNotFound, "Not found", nil); got.StatusCode != http.StatusNotFound || got.Cookies != nil {
		t.Fatalf("html response = %#v", got)
	}
}

func TestExtraHeadersOverrideDefaults(t *testing.T) {
	response := TypedResponse(http.StatusOK, "", TextContentType, map[string]string{"Content-Type": "application/custom"})
	if got := response.Headers["Content-Type"]; got != "application/custom" {
		t.Fatalf("Content-Type = %q, want override to win", got)
	}
}

func TestRedirects(t *testing.T) {
	permanent := PermanentRedirect("/products", map[string]string{"Cache-Control": "public"})
	if permanent.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("permanent status = %d", permanent.StatusCode)
	}
	if permanent.Headers["Location"] != "/products" || permanent.Headers["Cache-Control"] != "public" {
		t.Fatalf("permanent headers = %v", permanent.Headers)
	}

	seeOther := SeeOther("/cart", []string{"a=1"}, nil)
	if seeOther.StatusCode != http.StatusSeeOther || seeOther.Headers["Location"] != "/cart" {
		t.Fatalf("see other = %#v", seeOther)
	}
	if len(seeOther.Cookies) != 1 {
		t.Fatalf("see other cookies = %v", seeOther.Cookies)
	}
}

func TestValidOriginSecret(t *testing.T) {
	t.Setenv(EnvOriginHeaderSecret, "origin-secret")
	digest, configured := OriginSecretDigestFromEnvironment()
	if !configured {
		t.Fatal("OriginSecretDigestFromEnvironment configured = false, want true")
	}

	withHeader := events.APIGatewayV2HTTPRequest{Headers: map[string]string{OriginSecretHeaderName: "origin-secret"}}
	if !ValidOriginSecret(withHeader, digest, true) {
		t.Fatal("ValidOriginSecret matching header = false, want true")
	}
	withWrongHeader := events.APIGatewayV2HTTPRequest{Headers: map[string]string{OriginSecretHeaderName: "wrong"}}
	if ValidOriginSecret(withWrongHeader, digest, true) {
		t.Fatal("ValidOriginSecret wrong header = true, want false")
	}
	if ValidOriginSecret(events.APIGatewayV2HTTPRequest{}, digest, true) {
		t.Fatal("ValidOriginSecret missing header = true, want false")
	}
}

func TestValidOriginSecretUnconfiguredFollowsEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	if !ValidOriginSecret(events.APIGatewayV2HTTPRequest{}, [32]byte{}, false) {
		t.Fatal("unconfigured non-production = false, want true")
	}
	t.Setenv("APP_ENV", "production")
	if ValidOriginSecret(events.APIGatewayV2HTTPRequest{}, [32]byte{}, false) {
		t.Fatal("unconfigured production = true, want false")
	}
}
