package admin

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"golang.org/x/crypto/bcrypt"
)

const testSessionSecret = "admin-session-test-secret-with-enough-entropy"

func TestLoginWrongPasswordUsesGenericErrorAndDoesNotSetSessionCookie(t *testing.T) {
	handler, _ := newAuthTestHandler(t)

	response, err := handler.Handle(context.Background(), adminFormRequest(http.MethodPost, "/admin/login", url.Values{
		"password": {string(testWrongPassword(t))},
	}))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	if !strings.Contains(response.Body, "Invalid credentials") {
		t.Fatalf("body = %q, want generic invalid credentials error", response.Body)
	}
	if strings.Contains(strings.ToLower(response.Body), "password hash") {
		t.Fatalf("body leaks credential detail: %q", response.Body)
	}
	if _, found := responseCookieValue(response, adminSessionCookieName); found {
		t.Fatalf("response cookies = %#v, want no admin session cookie", response.Cookies)
	}
	assertNoStore(t, response)
}

func TestLoginCorrectPasswordSetsAdminSessionAndCSRFTokenCookies(t *testing.T) {
	handler, _ := newAuthTestHandler(t)

	response := loginResponse(t, handler)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if response.Headers["Location"] != "/admin" {
		t.Fatalf("Location = %q, want /admin", response.Headers["Location"])
	}

	sessionCookie := responseCookie(t, response, adminSessionCookieName)
	for _, want := range []string{adminSessionCookieName + "=", "Path=/", "Max-Age=28800", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(sessionCookie, want) {
			t.Fatalf("session cookie = %q, missing %q", sessionCookie, want)
		}
	}
	if strings.Contains(sessionCookie, "Domain=") {
		t.Fatalf("session cookie = %q, want no Domain", sessionCookie)
	}

	csrfCookie := responseCookie(t, response, adminCSRFCookieName)
	for _, want := range []string{adminCSRFCookieName + "=", "Path=/", "Secure", "SameSite=Lax"} {
		if !strings.Contains(csrfCookie, want) {
			t.Fatalf("csrf cookie = %q, missing %q", csrfCookie, want)
		}
	}
	if strings.Contains(csrfCookie, "Domain=") || strings.Contains(csrfCookie, "HttpOnly") {
		t.Fatalf("csrf cookie = %q, want no Domain and no HttpOnly", csrfCookie)
	}
	assertNoStore(t, response)
}

func TestProtectedAdminRootRequiresAuthenticatedSession(t *testing.T) {
	handler, _ := newAuthTestHandler(t)

	response, err := handler.Handle(context.Background(), adminRequest(http.MethodGet, "/admin"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if response.Headers["Location"] != "/admin/login" {
		t.Fatalf("Location = %q, want /admin/login", response.Headers["Location"])
	}
	for _, blocked := range []string{"Admin dashboard", "data-testid=\"admin-nav\"", "/admin/products", "/admin/categories", "/admin/logout"} {
		if strings.Contains(response.Body, blocked) {
			t.Fatalf("unauthenticated response body rendered protected content %q: %q", blocked, response.Body)
		}
	}
	assertNoStore(t, response)
}

func TestDashboardServesAuthenticatedShellFromRequestCookies(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	login := loginResponse(t, handler)

	request := adminRequest(http.MethodGet, "/admin")
	request.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName)), cookiePair(t, responseCookie(t, login, adminCSRFCookieName))}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertAdminDashboardShell(t, response.Body)
	if _, found := responseCookieValue(response, adminCSRFCookieName); found {
		t.Fatalf("response cookies = %#v, want existing valid CSRF cookie preserved", response.Cookies)
	}
	assertNoStore(t, response)
}

func TestDashboardRefreshesMissingCSRFTokenCookie(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	login := loginResponse(t, handler)

	request := adminRequest(http.MethodGet, "/admin")
	request.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName))}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertAdminDashboardShell(t, response.Body)
	csrfCookie := responseCookie(t, response, adminCSRFCookieName)
	csrfPair := cookiePair(t, csrfCookie)
	_, csrfValue, _ := strings.Cut(csrfPair, "=")
	if !strings.Contains(response.Body, `name="csrf_token" value="`+csrfValue+`"`) {
		t.Fatalf("body missing refreshed CSRF token from response cookie")
	}
	assertNoStore(t, response)
}

func TestProtectedAdminProxyPathAcceptsRawCookieHeader(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	login := loginResponse(t, handler)

	request := adminRequest(http.MethodGet, "/admin/catalog")
	request.Headers = map[string]string{"Cookie": cookiePair(t, responseCookie(t, login, adminSessionCookieName)) + "; " + cookiePair(t, responseCookie(t, login, adminCSRFCookieName))}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertAdminDashboardShell(t, response.Body)
}

func TestAdminSessionRejectsExpiredAndTamperedCookies(t *testing.T) {
	handler, currentTime := newAuthTestHandler(t)
	login := loginResponse(t, handler)
	sessionPair := cookiePair(t, responseCookie(t, login, adminSessionCookieName))

	*currentTime = currentTime.Add(adminSessionTTL + time.Second)
	expiredRequest := adminRequest(http.MethodGet, "/admin")
	expiredRequest.Cookies = []string{sessionPair}
	expiredResponse, err := handler.Handle(context.Background(), expiredRequest)
	if err != nil {
		t.Fatalf("Handle expired returned error: %v", err)
	}
	if expiredResponse.StatusCode != http.StatusSeeOther || expiredResponse.Headers["Location"] != "/admin/login" {
		t.Fatalf("expired response = (%d, %q), want redirect to login", expiredResponse.StatusCode, expiredResponse.Headers["Location"])
	}

	*currentTime = currentTime.Add(-adminSessionTTL - time.Second)
	tamperedRequest := adminRequest(http.MethodGet, "/admin")
	tamperedRequest.Cookies = []string{sessionPair + "x"}
	tamperedResponse, err := handler.Handle(context.Background(), tamperedRequest)
	if err != nil {
		t.Fatalf("Handle tampered returned error: %v", err)
	}
	if tamperedResponse.StatusCode != http.StatusSeeOther || tamperedResponse.Headers["Location"] != "/admin/login" {
		t.Fatalf("tampered response = (%d, %q), want redirect to login", tamperedResponse.StatusCode, tamperedResponse.Headers["Location"])
	}
}

func TestLogoutRequiresValidCSRFAndClearsCookies(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	login := loginResponse(t, handler)
	sessionPair := cookiePair(t, responseCookie(t, login, adminSessionCookieName))
	csrfPair := cookiePair(t, responseCookie(t, login, adminCSRFCookieName))
	_, csrfToken, _ := strings.Cut(csrfPair, "=")

	missingCSRFRequest := adminRequest(http.MethodPost, "/admin/logout")
	missingCSRFRequest.Cookies = []string{sessionPair, csrfPair}
	missingCSRFResponse, err := handler.Handle(context.Background(), missingCSRFRequest)
	if err != nil {
		t.Fatalf("Handle missing CSRF returned error: %v", err)
	}
	if missingCSRFResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want %d", missingCSRFResponse.StatusCode, http.StatusForbidden)
	}
	if _, found := responseCookieValue(missingCSRFResponse, adminSessionCookieName); found {
		t.Fatalf("missing CSRF cookies = %#v, want no clearing or replacement session cookie", missingCSRFResponse.Cookies)
	}

	invalidCSRFRequest := adminFormRequest(http.MethodPost, "/admin/logout", url.Values{"csrf_token": {csrfToken + "x"}})
	invalidCSRFRequest.Cookies = []string{sessionPair, csrfPair}
	invalidCSRFResponse, err := handler.Handle(context.Background(), invalidCSRFRequest)
	if err != nil {
		t.Fatalf("Handle invalid CSRF returned error: %v", err)
	}
	if invalidCSRFResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid CSRF status = %d, want %d", invalidCSRFResponse.StatusCode, http.StatusForbidden)
	}

	logoutRequest := adminRequest(http.MethodPost, "/admin/logout")
	logoutRequest.Cookies = []string{sessionPair, csrfPair}
	logoutRequest.Headers = map[string]string{"X-CSRF-Token": csrfToken}
	logoutResponse, err := handler.Handle(context.Background(), logoutRequest)
	if err != nil {
		t.Fatalf("Handle logout returned error: %v", err)
	}
	if logoutResponse.StatusCode != http.StatusSeeOther || logoutResponse.Headers["Location"] != "/admin/login" {
		t.Fatalf("logout response = (%d, %q), want redirect to login", logoutResponse.StatusCode, logoutResponse.Headers["Location"])
	}
	if cookie := responseCookie(t, logoutResponse, adminSessionCookieName); !strings.Contains(cookie, "Max-Age=0") {
		t.Fatalf("logout session cookie = %q, want clearing cookie", cookie)
	}
	if cookie := responseCookie(t, logoutResponse, adminCSRFCookieName); !strings.Contains(cookie, "Max-Age=0") {
		t.Fatalf("logout csrf cookie = %q, want clearing cookie", cookie)
	}
	assertNoStore(t, logoutResponse)
}

func TestAuthenticatedAdminPostRequiresCSRFBeforeMethodRejection(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	login := loginResponse(t, handler)
	sessionPair := cookiePair(t, responseCookie(t, login, adminSessionCookieName))
	csrfPair := cookiePair(t, responseCookie(t, login, adminCSRFCookieName))
	_, csrfToken, _ := strings.Cut(csrfPair, "=")

	missingCSRFRequest := adminRequest(http.MethodPost, "/admin/catalog")
	missingCSRFRequest.Cookies = []string{sessionPair, csrfPair}
	missingCSRFResponse, err := handler.Handle(context.Background(), missingCSRFRequest)
	if err != nil {
		t.Fatalf("Handle missing CSRF returned error: %v", err)
	}
	if missingCSRFResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, want %d", missingCSRFResponse.StatusCode, http.StatusForbidden)
	}

	validCSRFRequest := adminRequest(http.MethodPost, "/admin/catalog")
	validCSRFRequest.Cookies = []string{sessionPair, csrfPair}
	validCSRFRequest.Headers = map[string]string{"X-CSRF-Token": csrfToken}
	validCSRFResponse, err := handler.Handle(context.Background(), validCSRFRequest)
	if err != nil {
		t.Fatalf("Handle valid CSRF returned error: %v", err)
	}
	if validCSRFResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("valid CSRF status = %d, want %d", validCSRFResponse.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestAdminLoginLogoutAndProtectedRoutesEnforceMethods(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	login := loginResponse(t, handler)
	request := adminRequest(http.MethodDelete, "/admin")
	request.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName))}

	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
	}
	if response.Headers["Allow"] != "GET, HEAD" {
		t.Fatalf("Allow = %q, want GET, HEAD", response.Headers["Allow"])
	}

	logoutGetResponse, err := handler.Handle(context.Background(), adminRequest(http.MethodGet, "/admin/logout"))
	if err != nil {
		t.Fatalf("Handle logout GET returned error: %v", err)
	}
	if logoutGetResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("logout GET status = %d, want %d", logoutGetResponse.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestAdminHeadOmitsBody(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	login := loginResponse(t, handler)
	request := adminRequest(http.MethodHead, "/admin")
	request.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName)), cookiePair(t, responseCookie(t, login, adminCSRFCookieName))}

	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if response.Body != "" {
		t.Fatalf("body = %q, want empty", response.Body)
	}
}

func TestAdminResponsesSetNoStore(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	login := loginResponse(t, handler)
	authenticatedRequest := adminRequest(http.MethodGet, "/admin")
	authenticatedRequest.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName))}

	requests := []events.APIGatewayV2HTTPRequest{
		adminRequest(http.MethodGet, "/admin/login"),
		adminRequest(http.MethodGet, "/admin"),
		adminRequest(http.MethodGet, "/admin/"),
		authenticatedRequest,
	}
	for _, request := range requests {
		response, err := handler.Handle(context.Background(), request)
		if err != nil {
			t.Fatalf("Handle %s %s returned error: %v", request.RequestContext.HTTP.Method, request.RawPath, err)
		}
		assertNoStore(t, response)
	}
}

func TestHandleReturnsNotFoundOutsideAdmin(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	response, err := handler.Handle(context.Background(), adminRequest(http.MethodGet, "/products"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestCredentialsLoadFromLocalEnvironment(t *testing.T) {
	passwordHash := testPasswordHash(t)
	t.Setenv(EnvAdminPasswordHash, passwordHash)
	t.Setenv(EnvAdminSessionSecret, testSessionSecret)

	credentials, err := CredentialsFromEnvironment(context.Background())
	if err != nil {
		t.Fatalf("CredentialsFromEnvironment returned error: %v", err)
	}
	if credentials.PasswordHash != passwordHash {
		t.Fatalf("PasswordHash = %q, want configured hash", credentials.PasswordHash)
	}
	if credentials.SessionSecret != testSessionSecret {
		t.Fatalf("SessionSecret = %q, want configured secret", credentials.SessionSecret)
	}
}

func TestCredentialsLoadFromSecretsManagerJSONShape(t *testing.T) {
	passwordHash := testPasswordHash(t)
	t.Setenv(EnvAdminCredentialsSecretJSON, `{"password_hash":`+strconvQuote(passwordHash)+`,"session_secret":`+strconvQuote(testSessionSecret)+`}`)

	credentials, err := CredentialsFromEnvironment(context.Background())
	if err != nil {
		t.Fatalf("CredentialsFromEnvironment returned error: %v", err)
	}
	if credentials.PasswordHash != passwordHash {
		t.Fatalf("PasswordHash = %q, want configured hash", credentials.PasswordHash)
	}
	if credentials.SessionSecret != testSessionSecret {
		t.Fatalf("SessionSecret = %q, want configured secret", credentials.SessionSecret)
	}
}

func newAuthTestHandler(t *testing.T) (*Handler, *time.Time) {
	t.Helper()
	currentTime := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	handler := NewHandlerWithCredentials(Credentials{
		PasswordHash:  testPasswordHash(t),
		SessionSecret: testSessionSecret,
	})
	handler.now = func() time.Time { return currentTime }
	return handler, &currentTime
}

func testPasswordHash(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword(testPassword(t), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword returned error: %v", err)
	}
	return string(hash)
}

func testPassword(t *testing.T) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	return sum[:]
}

func testWrongPassword(t *testing.T) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte("wrong-" + t.Name()))
	return sum[:]
}

func loginResponse(t *testing.T, handler *Handler) events.APIGatewayV2HTTPResponse {
	t.Helper()
	response, err := handler.Handle(context.Background(), adminFormRequest(http.MethodPost, "/admin/login", url.Values{
		"password": {string(testPassword(t))},
	}))
	if err != nil {
		t.Fatalf("Handle login returned error: %v", err)
	}
	return response
}

func adminRequest(method string, path string) events.APIGatewayV2HTTPRequest {
	return events.APIGatewayV2HTTPRequest{
		RawPath: path,
		RequestContext: events.APIGatewayV2HTTPRequestContext{
			HTTP: events.APIGatewayV2HTTPRequestContextHTTPDescription{
				Method: method,
				Path:   path,
			},
		},
	}
}

func adminFormRequest(method string, path string, values url.Values) events.APIGatewayV2HTTPRequest {
	request := adminRequest(method, path)
	request.Headers = map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	request.Body = values.Encode()
	return request
}

func responseCookie(t *testing.T, response events.APIGatewayV2HTTPResponse, name string) string {
	t.Helper()
	for _, cookie := range response.Cookies {
		if strings.HasPrefix(cookie, name+"=") {
			return cookie
		}
	}
	t.Fatalf("response cookies = %#v, missing %s", response.Cookies, name)
	return ""
}

func responseCookieValue(response events.APIGatewayV2HTTPResponse, name string) (string, bool) {
	for _, cookie := range response.Cookies {
		if value, found := namedCookieValue(cookie, name); found {
			return value, true
		}
	}
	return "", false
}

func cookiePair(t *testing.T, setCookie string) string {
	t.Helper()
	pair, _, found := strings.Cut(setCookie, ";")
	if !found {
		t.Fatalf("set cookie = %q, want attributes", setCookie)
	}
	return pair
}

func assertNoStore(t *testing.T, response events.APIGatewayV2HTTPResponse) {
	t.Helper()
	if response.Headers["Cache-Control"] != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", response.Headers["Cache-Control"])
	}
}

func assertAdminDashboardShell(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{
		`<title>Admin dashboard | Thailand Gift Shop</title>`,
		`data-testid="admin-shell"`,
		`data-testid="admin-nav"`,
		`aria-label="Admin navigation"`,
		`href="/admin"`,
		`Dashboard`,
		`href="/admin/products"`,
		`Products`,
		`href="/admin/categories"`,
		`Categories`,
		`method="post" action="/admin/logout"`,
		`name="csrf_token"`,
		`data-testid="admin-logout-button"`,
		`Logout`,
		`<main id="admin-main"`,
		`Admin dashboard`,
		`Product workspace`,
		`Category workspace`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %q", want, body)
		}
	}
}

func strconvQuote(value string) string {
	quoted := strings.ReplaceAll(value, `\`, `\\`)
	quoted = strings.ReplaceAll(quoted, `"`, `\"`)
	return `"` + quoted + `"`
}
