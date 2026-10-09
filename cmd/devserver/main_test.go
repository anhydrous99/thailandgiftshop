package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/ssr"
)

const devserverTestCartSecret = "devserver-cart-test-secret"

func TestNewDevServerHandlerUsesDemoStoreWhenEnabled(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "1")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	store, ok := handler.Catalog().(*catalog.MemoryStore)
	if !ok {
		t.Fatalf("handler catalog store = %T, want *catalog.MemoryStore", handler.Catalog())
	}

	products, err := store.ListActiveProducts(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListActiveProducts returned error: %v", err)
	}
	if len(products) != 11 {
		t.Fatalf("active demo product count = %d, want 11", len(products))
	}
}

func TestNewDevServerHandlerKeepsEnvironmentStoreByDefault(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	if _, ok := handler.Catalog().(catalog.EmptyStore); !ok {
		t.Fatalf("handler catalog store = %T, want catalog.EmptyStore", handler.Catalog())
	}
}

func TestNewDevServerHandlerRequiresExactDemoStoreFlag(t *testing.T) {
	t.Setenv(envDemoCatalogStore, "true")
	t.Setenv(catalog.EnvTableName, "")

	handler, err := newDevServerHandler(context.Background())
	if err != nil {
		t.Fatalf("newDevServerHandler returned error: %v", err)
	}
	if _, ok := handler.Catalog().(catalog.EmptyStore); !ok {
		t.Fatalf("handler catalog store = %T, want catalog.EmptyStore", handler.Catalog())
	}
}

func TestDevserverForwardsPostBodyAndRequestCookies(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, devserverTestCartSecret)
	handler := ssr.NewHandler(devserverCartStore())
	encoded, err := cart.EncodeCookie(mustDevserverCart(t, []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}}), devserverTestCartSecret)
	if err != nil {
		t.Fatalf("EncodeCookie returned error: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/cart/items", strings.NewReader("slug=mango-sticky-rice-kit&quantity=2"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: cart.CookieName, Value: encoded})
	responseRecorder := httptest.NewRecorder()

	handleSSR(handler, responseRecorder, request)

	response := responseRecorder.Result()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	setCookie := response.Header.Get("Set-Cookie")
	decoded := cart.DecodeCookie(devserverCookieValue(t, setCookie), devserverTestCartSecret)
	if decoded.NeedsClear {
		t.Fatal("decoded response cart NeedsClear = true, want false")
	}
	lines := decoded.Cart.Lines()
	if len(lines) != 1 || lines[0].Slug != "mango-sticky-rice-kit" || lines[0].Quantity != 3 {
		t.Fatalf("cart lines = %#v, want forwarded cookie quantity plus POST body quantity", lines)
	}
}

func TestDevserverWritesApiResponseCookies(t *testing.T) {
	t.Setenv(cart.EnvCookieSecret, devserverTestCartSecret)
	handler := ssr.NewHandler(devserverCartStore())
	request := httptest.NewRequest(http.MethodPost, "/cart/items", strings.NewReader("slug=mango-sticky-rice-kit&quantity=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	responseRecorder := httptest.NewRecorder()

	handleSSR(handler, responseRecorder, request)

	setCookies := responseRecorder.Result().Header.Values("Set-Cookie")
	if len(setCookies) != 1 {
		t.Fatalf("Set-Cookie headers = %#v, want one", setCookies)
	}
	if !strings.Contains(setCookies[0], cart.CookieName+"=") || !strings.Contains(setCookies[0], "HttpOnly") {
		t.Fatalf("Set-Cookie = %q, want serialized cart cookie", setCookies[0])
	}
}

func TestFakeEmailCaptureEndpoint(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	t.Setenv(email.EnvSenderMode, email.SenderKindFake)
	now := time.Date(2026, 6, 11, 10, 30, 0, 0, time.UTC)
	fakeSender := email.NewFakeSenderWithClock(func() time.Time { return now })
	sent, err := fakeSender.Send(context.Background(), email.Message{
		To:       "shopper@example.test",
		Subject:  "Order email",
		Text:     "Plain text body",
		HTML:     "<p>HTML body</p>",
		Kind:     email.MessageKindOrderPlaced,
		EventKey: "order:order_123:placed",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	email.UseFakeSenderForEnvironment(fakeSender)
	t.Cleanup(func() { email.UseFakeSenderForEnvironment(nil) })
	configuredSender, err := email.NewSenderFromEnvironment(context.Background())
	if err != nil {
		t.Fatalf("NewSenderFromEnvironment returned error: %v", err)
	}
	if configuredSender != fakeSender {
		t.Fatalf("configured fake sender = %p, want shared devserver sender %p", configuredSender, fakeSender)
	}
	mux := newDevServerMux(devServerHandlers{fakeEmails: fakeSender}, t.TempDir(), t.TempDir())

	getRequest := httptest.NewRequest(http.MethodGet, "/__test/emails", nil)
	getRequest.Header.Set("Cookie", "session=must-not-appear")
	getRequest.Header.Set("Authorization", "Bearer must-not-appear")
	getRecorder := httptest.NewRecorder()
	mux.ServeHTTP(getRecorder, getRequest)

	getResponse := getRecorder.Result()
	if getResponse.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getResponse.StatusCode, http.StatusOK)
	}
	if contentType := getResponse.Header.Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("GET Content-Type = %q, want application/json", contentType)
	}
	body := readDevserverResponseBody(t, getResponse)
	for _, forbidden := range []string{"must-not-appear", "Cookie", "Authorization"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("GET body contains forbidden capture data %q: %s", forbidden, body)
		}
	}
	var messages []map[string]string
	if err := json.Unmarshal([]byte(body), &messages); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	assertFakeEmailCaptureFields(t, messages[0])
	if got := messages[0]["id"]; got != sent.ID {
		t.Fatalf("id = %q, want %q", got, sent.ID)
	}
	if got := messages[0]["to"]; got != "shopper@example.test" {
		t.Fatalf("to = %q, want shopper@example.test", got)
	}
	if got := messages[0]["kind"]; got != email.MessageKindOrderPlaced {
		t.Fatalf("kind = %q, want %q", got, email.MessageKindOrderPlaced)
	}
	if got := messages[0]["eventKey"]; got != "order:order_123:placed" {
		t.Fatalf("eventKey = %q, want order:order_123:placed", got)
	}
	if got := messages[0]["createdAt"]; got != now.Format(time.RFC3339) {
		t.Fatalf("createdAt = %q, want %q", got, now.Format(time.RFC3339))
	}
	otherSent, err := fakeSender.Send(context.Background(), email.Message{
		To:       "other@example.test",
		Subject:  "Other order email",
		Text:     "Other plain text body",
		HTML:     "<p>Other HTML body</p>",
		Kind:     email.MessageKindOrderPlaced,
		EventKey: "order:order_456:placed",
	})
	if err != nil {
		t.Fatalf("Send other returned error: %v", err)
	}

	scopedClearRecorder := httptest.NewRecorder()
	mux.ServeHTTP(scopedClearRecorder, httptest.NewRequest(http.MethodPost, "/__test/emails/clear?to=shopper@example.test", nil))
	if scopedClearRecorder.Result().StatusCode != http.StatusNoContent {
		t.Fatalf("scoped clear status = %d, want %d", scopedClearRecorder.Result().StatusCode, http.StatusNoContent)
	}

	afterScopedClearRecorder := httptest.NewRecorder()
	mux.ServeHTTP(afterScopedClearRecorder, httptest.NewRequest(http.MethodGet, "/__test/emails", nil))
	var afterScopedClear []map[string]string
	if err := json.Unmarshal([]byte(readDevserverResponseBody(t, afterScopedClearRecorder.Result())), &afterScopedClear); err != nil {
		t.Fatalf("decode after scoped clear body: %v", err)
	}
	if len(afterScopedClear) != 1 || afterScopedClear[0]["id"] != otherSent.ID {
		t.Fatalf("after scoped clear messages = %#v, want only other recipient", afterScopedClear)
	}

	clearRecorder := httptest.NewRecorder()
	mux.ServeHTTP(clearRecorder, httptest.NewRequest(http.MethodPost, "/__test/emails/clear", nil))
	if clearRecorder.Result().StatusCode != http.StatusNoContent {
		t.Fatalf("clear status = %d, want %d", clearRecorder.Result().StatusCode, http.StatusNoContent)
	}

	afterClearRecorder := httptest.NewRecorder()
	mux.ServeHTTP(afterClearRecorder, httptest.NewRequest(http.MethodGet, "/__test/emails", nil))
	var afterClear []map[string]string
	if err := json.Unmarshal([]byte(readDevserverResponseBody(t, afterClearRecorder.Result())), &afterClear); err != nil {
		t.Fatalf("decode after clear body: %v", err)
	}
	if len(afterClear) != 0 {
		t.Fatalf("after clear message count = %d, want 0", len(afterClear))
	}

	t.Setenv(email.EnvSenderMode, email.SenderKindSES)
	for _, endpoint := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/__test/emails"},
		{method: http.MethodPost, path: "/__test/emails/clear"},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(endpoint.method, endpoint.path, nil))
		if recorder.Result().StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status in non-fake mode = %d, want %d", endpoint.method, endpoint.path, recorder.Result().StatusCode, http.StatusNotFound)
		}
	}
}

func TestFakeEmailCaptureEndpointProductionGuard(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
	t.Setenv(email.EnvSenderMode, email.SenderKindFake)
	fakeSender := email.NewFakeSender()
	_, err := fakeSender.Send(context.Background(), email.Message{
		To:      "shopper@example.test",
		Subject: "Sensitive fake message",
		Text:    "production guard must hide this",
		HTML:    "<p>production guard must hide this</p>",
		Kind:    email.MessageKindPasswordReset,
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	mux := newDevServerMux(devServerHandlers{fakeEmails: fakeSender}, t.TempDir(), t.TempDir())

	for _, endpoint := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/__test/emails"},
		{method: http.MethodPost, path: "/__test/emails/clear"},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(endpoint.method, endpoint.path, nil))
		response := recorder.Result()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want %d", endpoint.method, endpoint.path, response.StatusCode, http.StatusNotFound)
		}
		if body := readDevserverResponseBody(t, response); strings.Contains(body, "Sensitive fake message") {
			t.Fatalf("%s %s body exposed captured message: %s", endpoint.method, endpoint.path, body)
		}
	}
}

func TestRequestSourceIPParsesRemoteAddrHostPort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "203.0.113.10:53124"

	if sourceIP := requestSourceIP(request); sourceIP != "203.0.113.10" {
		t.Fatalf("requestSourceIP = %q, want IPv4 host", sourceIP)
	}
}

func TestRequestSourceIPParsesIPv6RemoteAddrHostPort(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "[2001:db8::10]:53124"

	if sourceIP := requestSourceIP(request); sourceIP != "2001:db8::10" {
		t.Fatalf("requestSourceIP = %q, want IPv6 host", sourceIP)
	}
}

func TestRequestSourceIPFallsBackForMalformedRemoteAddr(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "malformed"

	if sourceIP := requestSourceIP(request); sourceIP != "malformed" {
		t.Fatalf("requestSourceIP = %q, want malformed fallback", sourceIP)
	}
}

func TestREADMEAdminBootstrapDocsUsePlaceholdersOnly(t *testing.T) {
	requiredByDocument := map[string][]string{
		"README.md":            {"docs/configuration.md", "docs/development.md", "docs/deployment.md", "docs/operations.md", "docs/architecture.md"},
		"docs/development.md":  {"CATALOG_DEMO_STORE=1", "without a live AWS account", "CUSTOMER_SESSION_SECRET"},
		"docs/deployment.md":   {"thailandgiftshop/admin/credentials", "thailandgiftshop/stripe/credentials"},
		"docs/architecture.md": {"ThailandGiftshopStack", "us-east-1"},
		"docs/operations.md":   {"Edge-log invariants", "query strings"},
		"docs/configuration.md": {
			"ADMIN_PASSWORD_HASH=<bcrypt-hash>",
			"ADMIN_SESSION_SECRET=<session-secret>",
			"thailandgiftshop/admin/credentials",
			"ADMIN_CREDENTIALS_SECRET_JSON",
			"ADMIN_CREDENTIALS_SECRET_NAME",
			"STRIPE_CREDENTIALS_SECRET_JSON",
			"STRIPE_CREDENTIALS_SECRET_NAME",
			"CUSTOMER_SESSION_SECRET",
		},
	}

	requiredJSONByDocument := map[string][]map[string]string{
		"docs/configuration.md": {
			{"password_hash": "<bcrypt-hash>", "session_secret": "<session-secret>"},
			{"secret_key": "<sk-test-key>", "webhook_signing_secret": "<whsec>"},
		},
		"docs/deployment.md": {
			{"password_hash": "<bcrypt-hash>", "session_secret": "<session-secret>"},
			{"secret_key": "<sk-live-or-test-key>", "webhook_signing_secret": "<whsec-placeholder>"},
		},
	}
	jsonExample := regexp.MustCompile(`\{[^{}]*\}`)
	// Real-looking secret material must never land in these documents: Stripe
	// secret keys, Stripe webhook signing secrets followed by token
	// characters (the bare `whsec_` prefix in prose is fine), and bcrypt
	// hashes. Docs must use <placeholder> forms instead.
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`sk_live_`),
		regexp.MustCompile(`sk_test_[0-9A-Za-z]{8,}`),
		regexp.MustCompile(`whsec_[0-9A-Za-z]{8,}`),
		regexp.MustCompile(`\$2[aby]\$\d{2}\$`),
	}
	for document, required := range requiredByDocument {
		t.Run(document, func(t *testing.T) {
			data, err := os.ReadFile("../../" + document)
			if err != nil {
				t.Fatalf("read documentation: %v", err)
			}
			content := string(data)
			for _, text := range required {
				if !strings.Contains(content, text) {
					t.Errorf("missing required documentation %q", text)
				}
			}
			for _, expected := range requiredJSONByDocument[document] {
				found := false
				for _, example := range jsonExample.FindAllString(content, -1) {
					var fields map[string]string
					if json.Unmarshal([]byte(example), &fields) != nil || len(fields) != len(expected) {
						continue
					}
					matches := true
					for key, value := range expected {
						if fields[key] != value {
							matches = false
						}
					}
					found = found || matches
				}
				if !found {
					t.Error("missing required placeholder JSON example")
				}
			}
			for _, pattern := range forbidden {
				if pattern.MatchString(content) {
					t.Error("documentation contains real-looking secret material; use placeholders")
				}
			}
		})
	}
}

func devserverCartStore() catalog.Store {
	return catalog.NewMemoryStore([]catalog.Product{
		{
			Slug:          "mango-sticky-rice-kit",
			Name:          "Mango Sticky Rice Treats",
			Status:        catalog.StatusActive,
			StockQuantity: 5,
		},
	}, nil)
}

func mustDevserverCart(t *testing.T, lines []cart.Line) cart.Cart {
	t.Helper()
	testCart, err := cart.New(lines)
	if err != nil {
		t.Fatalf("cart.New returned error: %v", err)
	}
	return testCart
}

func devserverCookieValue(t *testing.T, cookieHeader string) string {
	t.Helper()
	for part := range strings.SplitSeq(cookieHeader, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name == cart.CookieName {
			return value
		}
	}
	t.Fatalf("cart cookie missing from %q", cookieHeader)
	return ""
}

func readDevserverResponseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}

func assertFakeEmailCaptureFields(t *testing.T, message map[string]string) {
	t.Helper()
	allowedFields := map[string]bool{
		"id":        true,
		"to":        true,
		"subject":   true,
		"text":      true,
		"html":      true,
		"kind":      true,
		"eventKey":  true,
		"createdAt": true,
	}
	if len(message) != len(allowedFields) {
		t.Fatalf("field count = %d, want %d: %#v", len(message), len(allowedFields), message)
	}
	for field := range message {
		if !allowedFields[field] {
			t.Fatalf("unexpected field %q in fake email capture message", field)
		}
	}
}
