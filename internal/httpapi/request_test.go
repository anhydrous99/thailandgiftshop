package httpapi

import (
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestMethodAndPathDefaults(t *testing.T) {
	empty := events.APIGatewayV2HTTPRequest{}
	if got := Method(empty); got != http.MethodGet {
		t.Fatalf("Method(empty) = %q, want GET", got)
	}
	if got := Path(empty); got != "/" {
		t.Fatalf("Path(empty) = %q, want /", got)
	}

	request := events.APIGatewayV2HTTPRequest{RawPath: "/products"}
	request.RequestContext.HTTP.Method = http.MethodPost
	request.RequestContext.HTTP.Path = "/ignored-when-rawpath-set"
	if got := Method(request); got != http.MethodPost {
		t.Fatalf("Method = %q, want POST", got)
	}
	if got := Path(request); got != "/products" {
		t.Fatalf("Path = %q, want /products", got)
	}

	contextOnly := events.APIGatewayV2HTTPRequest{}
	contextOnly.RequestContext.HTTP.Path = "/from-context"
	if got := Path(contextOnly); got != "/from-context" {
		t.Fatalf("Path = %q, want /from-context", got)
	}
}

func TestHeaderValueMatchesCaseInsensitively(t *testing.T) {
	headers := map[string]string{"X-Custom-Header": "value"}
	if got := HeaderValue(headers, "x-custom-header"); got != "value" {
		t.Fatalf("HeaderValue = %q, want value", got)
	}
	if got := HeaderValue(headers, "missing"); got != "" {
		t.Fatalf("HeaderValue missing = %q, want empty", got)
	}
}

func TestCookieValueReadsCookiesSliceAndHeaderFallback(t *testing.T) {
	request := events.APIGatewayV2HTTPRequest{
		Cookies: []string{"first=1; second=2", "third=3"},
	}
	if value, found := CookieValue(request, "third"); !found || value != "3" {
		t.Fatalf("CookieValue third = %q/%t, want 3/true", value, found)
	}
	if value, found := CookieValue(request, "second"); !found || value != "2" {
		t.Fatalf("CookieValue second = %q/%t, want 2/true", value, found)
	}

	headerOnly := events.APIGatewayV2HTTPRequest{
		Headers: map[string]string{"Cookie": "session=abc; other=def"},
	}
	if value, found := CookieValue(headerOnly, "session"); !found || value != "abc" {
		t.Fatalf("CookieValue header fallback = %q/%t, want abc/true", value, found)
	}
	if _, found := CookieValue(headerOnly, "missing"); found {
		t.Fatal("CookieValue missing cookie found = true, want false")
	}
}

func TestFormValuesDecodesBase64Bodies(t *testing.T) {
	plain := events.APIGatewayV2HTTPRequest{Body: "slug=mango&quantity=2"}
	values, err := FormValues(plain)
	if err != nil {
		t.Fatalf("FormValues plain returned error: %v", err)
	}
	if values.Get("slug") != "mango" || values.Get("quantity") != "2" {
		t.Fatalf("FormValues plain = %v", values)
	}

	encoded := events.APIGatewayV2HTTPRequest{
		Body:            base64.StdEncoding.EncodeToString([]byte("slug=tea")),
		IsBase64Encoded: true,
	}
	values, err = FormValues(encoded)
	if err != nil {
		t.Fatalf("FormValues base64 returned error: %v", err)
	}
	if values.Get("slug") != "tea" {
		t.Fatalf("FormValues base64 = %v", values)
	}

	invalid := events.APIGatewayV2HTTPRequest{Body: "%%%", IsBase64Encoded: true}
	if _, err := FormValues(invalid); err == nil {
		t.Fatal("FormValues invalid base64 error = nil, want error")
	}
}

func TestJSONBodyRejectsUnknownFields(t *testing.T) {
	var target struct {
		Name string `json:"name"`
	}
	known := events.APIGatewayV2HTTPRequest{Body: `{"name":"mango"}`}
	if err := JSONBody(known, &target); err != nil {
		t.Fatalf("JSONBody returned error: %v", err)
	}
	if target.Name != "mango" {
		t.Fatalf("decoded name = %q, want mango", target.Name)
	}

	unknown := events.APIGatewayV2HTTPRequest{Body: `{"name":"mango","extra":true}`}
	if err := JSONBody(unknown, &target); err == nil {
		t.Fatal("JSONBody unknown field error = nil, want error")
	}
}
