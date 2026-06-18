package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/anhydrous99/thailandgiftshop/internal/staticassets"
	"github.com/aws/aws-lambda-go/events"
	"golang.org/x/crypto/bcrypt"
)

const adminRobotsTag = "noindex, nofollow"

const adminAllowedMethods = http.MethodGet + ", " + http.MethodHead
const loginAllowedMethods = http.MethodGet + ", " + http.MethodHead + ", " + http.MethodPost
const logoutAllowedMethods = http.MethodPost
const uploadAllowedMethods = http.MethodPost

const csrfHeaderName = "X-CSRF-Token"
const adminOriginHeaderName = httpapi.OriginSecretHeaderName
const cloudFrontViewerAddressHeaderName = "CloudFront-Viewer-Address"
const passwordFieldName = "password"
const csrfFieldName = "csrf_token"

const dummyBcryptHash = "$2a$04$Vn0nSllZrX4bNwFaMifZAuS4xCZ9oE4DngJ02k8pYDz7zlXzpOfgK"

var allowedAdminLoginHosts = map[string]struct{}{
	"thailandgiftshop.com":     {},
	"www.thailandgiftshop.com": {},
	"localhost":                {},
	"127.0.0.1":                {},
}

// adminCredentialsTTL bounds how long a warm Lambda reuses cached credentials
// before reloading them from the environment (and, in production, re-reading
// the Secrets Manager secret). It is the window within which a password
// rotation takes effect without a redeploy or cold start.
const adminCredentialsTTL = 5 * time.Minute

type Handler struct {
	stateMu               sync.RWMutex
	credentials           Credentials
	credentialsExpiry     time.Time
	now                   func() time.Time
	catalog               catalog.AdminStore
	commerce              commerce.Store
	stock                 catalog.StockStore
	payments              payments.Provider
	emailSender           email.Sender
	uploads               productImageUploads
	loginThrottle         adminLoginThrottle
	metrics               observability.Recorder
	customerSessionSecret string
	originSecretDigests   httpapi.OriginSecretDigests
	originSecretSet       bool
}

var adminColdStartRecorded atomic.Bool

func NewHandler() *Handler {
	return withOriginSecretFromEnvironment(&Handler{now: time.Now, loginThrottle: noopAdminLoginThrottle{}})
}

func NewHandlerWithCredentials(credentials Credentials) *Handler {
	return withOriginSecretFromEnvironment(&Handler{credentials: credentials, now: time.Now})
}

func withOriginSecretFromEnvironment(h *Handler) *Handler {
	h.originSecretDigests, h.originSecretSet = httpapi.OriginSecretDigestsFromEnvironment()
	return h
}

func NewHandlerWithCredentialsAndCatalog(credentials Credentials, adminStore catalog.AdminStore) *Handler {
	handler := NewHandlerWithCredentials(credentials)
	handler.catalog = adminStore
	return handler
}

func NewLocalDemoHandler(credentials Credentials, adminStore catalog.AdminStore, commerceStore commerce.Store, stockStore catalog.StockStore, paymentsProvider payments.Provider) *Handler {
	return NewLocalDemoHandlerWithEmailSender(credentials, adminStore, commerceStore, stockStore, paymentsProvider, nil)
}

func NewLocalDemoHandlerWithEmailSender(credentials Credentials, adminStore catalog.AdminStore, commerceStore commerce.Store, stockStore catalog.StockStore, paymentsProvider payments.Provider, emailSender email.Sender) *Handler {
	if emailSender == nil {
		emailSender = email.NewFakeSender()
	}
	handler := NewHandlerWithCredentialsAndCatalog(credentials, adminStore)
	handler.uploads = localProductImageUploads{}
	handler.commerce = commerceStore
	handler.stock = stockStore
	handler.payments = paymentsProvider
	handler.emailSender = emailSender
	handler.customerSessionSecret = strings.TrimSpace(os.Getenv(commerce.EnvSessionSecret))
	return handler
}

func NewHandlerFromEnvironment(ctx context.Context) (*Handler, error) {
	credentials, err := CredentialsFromEnvironment(ctx)
	if err != nil {
		return nil, err
	}

	metrics := observability.NewEMFRecorder(os.Stdout)
	adminStore, _, err := catalog.NewAdminStoreFromEnvWithRecorder(ctx, metrics)
	if err != nil {
		return nil, err
	}

	loginThrottle, err := adminLoginThrottleFromEnvironment(ctx, credentials.SessionSecret)
	if err != nil {
		return nil, err
	}

	commerceStore, _, err := commerce.NewStoreFromEnvWithRecorder(ctx, metrics)
	if err != nil {
		return nil, err
	}
	emailSender, err := email.NewSenderFromEnvironment(ctx)
	if err != nil {
		return nil, err
	}
	handler := NewHandlerWithCredentialsAndCatalog(credentials, adminStore)
	// Mark the cold-start credentials with a reload deadline so a later rotation
	// is picked up within adminCredentialsTTL on this warm container.
	handler.storeCredentials(credentials)
	handler.commerce = commerceStore
	handler.emailSender = emailSender
	handler.customerSessionSecret = strings.TrimSpace(os.Getenv(commerce.EnvSessionSecret))
	// The catalog admin store doubles as the stock store (both the Dynamo and
	// memory implementations satisfy catalog.StockStore), so admin order
	// cancellations release stock through the same versioned write path.
	if stockStore, ok := adminStore.(catalog.StockStore); ok {
		handler.stock = stockStore
	}
	if err := handler.configurePaymentsFromEnvironment(ctx); err != nil {
		return nil, err
	}
	handler.loginThrottle = loginThrottle
	handler.metrics = metrics
	return handler, nil
}

func (h *Handler) configurePaymentsFromEnvironment(ctx context.Context) error {
	provider, providerErr := payments.NewProviderFromEnvironment(ctx)
	if providerErr != nil {
		if appenv.IsProduction() {
			return fmt.Errorf("orders payments provider: %w", providerErr)
		}
		logAdminError("orders: payments provider unavailable; cancel skips session expiry", providerErr)
		return nil
	}
	h.payments = provider
	return nil
}

var defaultHandler = NewHandler()

func Handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return defaultHandler.Handle(ctx, request)
}

func (h *Handler) Handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	started := time.Now()
	method := httpapi.Method(request)
	route := h.adminMetricRoute(request)
	response, err := h.handle(ctx, request)
	h.recordRouteMetrics(started, route, method, response.StatusCode)
	return response, err
}

func (h *Handler) handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	path := httpapi.Path(request)
	if path != "/admin" && !strings.HasPrefix(path, "/admin/") {
		return httpapi.HTMLResponse(http.StatusNotFound, "Not found", nil), nil
	}
	if !h.validAdminOrigin(request) {
		h.recordAdminOriginRejected()
		return adminHTMLResponse(http.StatusForbidden, "Forbidden", nil, nil), nil
	}
	if path == "/admin/" {
		return adminRedirectResponse(http.StatusPermanentRedirect, "/admin", nil), nil
	}

	switch path {
	case "/admin/login":
		return h.handleLogin(ctx, request), nil
	case "/admin/logout":
		return h.handleLogout(ctx, request), nil
	default:
		return h.handleProtectedAdmin(ctx, request), nil
	}
}

func (h *Handler) adminMetricRoute(request events.APIGatewayV2HTTPRequest) string {
	path := httpapi.Path(request)
	if path != "/admin" && !strings.HasPrefix(path, "/admin/") {
		return "not_found"
	}
	if !h.validAdminOrigin(request) {
		return "origin_rejected"
	}
	if path == "/admin/login" {
		return "login"
	}
	if path == "/admin/logout" {
		return "logout"
	}
	if isAdminAnalyticsPath(path) {
		return "analytics"
	}
	if isAdminProductPath(path) {
		return "products"
	}
	if isAdminCategoryPath(path) {
		return "categories"
	}
	if isAdminOrderPath(path) {
		return "orders"
	}
	if isProductImageUploadPath(path) {
		return "uploads"
	}
	return "dashboard"
}

func (h *Handler) recordRouteMetrics(started time.Time, route string, method string, statusCode int) {
	if h.metrics == nil {
		return
	}
	if adminColdStartRecorded.CompareAndSwap(false, true) {
		h.metrics.Record(observability.Count(
			observability.MetricRouteColdStart,
			observability.Dim("Service", metricServiceAdmin),
		))
	}
	h.metrics.Record(observability.Duration(
		observability.MetricRouteDurationMs,
		time.Since(started),
		observability.Dim("Service", metricServiceAdmin),
		observability.Dim("Route", route),
		observability.Dim("Method", method),
		observability.Dim("Status", strconv.Itoa(statusCode)),
	))
}

func (h *Handler) handleLogin(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodPost {
		return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": loginAllowedMethods}, nil)
	}
	if method == http.MethodGet || method == http.MethodHead {
		body := loginPageBody("")
		if method == http.MethodHead {
			body = ""
		}
		return adminHTMLResponse(http.StatusOK, body, nil, nil)
	}
	if !validAdminLoginOrigin(request) {
		return adminHTMLResponse(http.StatusForbidden, "Forbidden", nil, nil)
	}

	credentials := h.credentialsForRequest(ctx)
	client := adminLoginClient(request)
	status, err := h.loginThrottleForRequest().ReserveAttempt(ctx, client, h.currentTime())
	if err != nil {
		logAdminError("login: reserve throttle attempt", err)
		h.recordAdminLoginAttempt(metricOutcomeError)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if !status.Allowed {
		h.recordAdminLoginAttempt(metricOutcomeThrottled)
		return adminLoginThrottleResponse(status, h.currentTime())
	}

	values, err := httpapi.FormValues(request)
	if err != nil || !h.validPasswordWithCredentials(credentials, values.Get(passwordFieldName)) {
		if status.Locked(h.currentTime()) {
			h.recordAdminLoginAttempt(metricOutcomeThrottled)
			return adminLoginThrottleResponse(status, h.currentTime())
		}
		h.recordAdminLoginAttempt(metricOutcomeInvalid)
		return adminHTMLResponse(http.StatusUnauthorized, loginPageBody("Invalid credentials"), nil, nil)
	}
	if err := h.loginThrottleForRequest().Clear(ctx, client); err != nil {
		logAdminError("login: clear throttle", err)
		h.recordAdminLoginAttempt(metricOutcomeError)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}

	session, sessionValue, err := newAdminSession(credentials.SessionSecret, h.currentTime())
	if err != nil {
		logAdminError("login: create session", err)
		h.recordAdminLoginAttempt(metricOutcomeError)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	csrfValue, err := newAdminCSRFToken(session, credentials.SessionSecret)
	if err != nil {
		logAdminError("login: create csrf token", err)
		h.recordAdminLoginAttempt(metricOutcomeError)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}

	h.recordAdminLoginAttempt(metricOutcomeSuccess)
	return adminRedirectResponse(http.StatusSeeOther, "/admin", []string{
		adminSessionCookie(sessionValue, session.ExpiresAt).String(),
		adminCSRFCookie(csrfValue, session.ExpiresAt).String(),
	})
}

func validAdminLoginOrigin(request events.APIGatewayV2HTTPRequest) bool {
	if origin := strings.TrimSpace(httpapi.HeaderValue(request.Headers, "Origin")); origin != "" {
		return adminLoginHostAllowed(origin)
	}
	if referer := strings.TrimSpace(httpapi.HeaderValue(request.Headers, "Referer")); referer != "" {
		return adminLoginHostAllowed(referer)
	}
	return !appenv.IsProduction()
}

func adminLoginHostAllowed(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	_, ok := allowedAdminLoginHosts[strings.ToLower(parsed.Hostname())]
	return ok
}

func (h *Handler) handleLogout(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	if httpapi.Method(request) != http.MethodPost {
		return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": logoutAllowedMethods}, nil)
	}

	session, authenticated := h.authenticatedSession(ctx, request)
	if !authenticated {
		return adminRedirectResponse(http.StatusSeeOther, "/admin/login", nil)
	}
	if !h.validCSRF(ctx, request, session) {
		return adminHTMLResponse(http.StatusForbidden, "Forbidden", nil, nil)
	}

	return adminRedirectResponse(http.StatusSeeOther, "/admin/login", []string{
		clearAdminSessionCookie().String(),
		clearAdminCSRFCookie().String(),
	})
}

func (h *Handler) handleProtectedAdmin(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	session, authenticated := h.authenticatedSession(ctx, request)
	if !authenticated {
		return adminRedirectResponse(http.StatusSeeOther, "/admin/login", nil)
	}

	path := httpapi.Path(request)
	method := httpapi.Method(request)
	if method == http.MethodPost && !h.validCSRF(ctx, request, session) {
		return adminHTMLResponse(http.StatusForbidden, "Forbidden", nil, nil)
	}
	if isAdminAnalyticsPath(path) {
		return h.handleAdminAnalytics(ctx, request, session)
	}
	if isAdminProductPath(path) {
		return h.handleAdminProducts(ctx, path, request, session)
	}
	if isAdminCategoryPath(path) {
		return h.handleAdminCategories(ctx, path, request, session)
	}
	if isAdminOrderPath(path) {
		return h.handleAdminOrders(ctx, path, request, session)
	}
	if isProductImageUploadPath(path) {
		if method != http.MethodPost {
			return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": uploadAllowedMethods}, nil)
		}
		return h.handleProductImageUpload(ctx, path, request, session)
	}
	if method != http.MethodGet && method != http.MethodHead {
		return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminAllowedMethods}, nil)
	}

	csrfValue, cookies, err := h.csrfForProtectedResponse(ctx, request, session)
	if err != nil {
		logAdminError("dashboard: issue csrf token", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	body, err := h.adminPageBody(ctx, csrfValue)
	if err != nil {
		logAdminError("dashboard: render", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if method == http.MethodHead {
		body = ""
	}

	return adminHTMLResponse(http.StatusOK, body, nil, cookies)
}

func (h *Handler) credentialsForRequest(ctx context.Context) Credentials {
	cached, fresh := h.cachedCredentials()
	if fresh {
		return cached
	}

	loadedCredentials, err := CredentialsFromEnvironment(ctx)
	if err != nil {
		// Keep serving with the last-known-good credentials rather than locking
		// admins out on a transient Secrets Manager error.
		logAdminError("load credentials from environment", err)
		return cached
	}

	h.storeCredentials(loadedCredentials)
	return loadedCredentials
}

// cachedCredentials returns the cached credentials and whether they may still be
// used without reloading. Empty credentials are never fresh. A zero expiry marks
// credentials injected directly (tests, local/demo) that have no reload source,
// so they never expire; a non-zero expiry is honored against the clock.
func (h *Handler) cachedCredentials() (Credentials, bool) {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	if h.credentials.PasswordHash == "" || h.credentials.SessionSecret == "" {
		return h.credentials, false
	}
	if h.credentialsExpiry.IsZero() {
		return h.credentials, true
	}
	return h.credentials, h.currentTime().Before(h.credentialsExpiry)
}

func (h *Handler) storeCredentials(credentials Credentials) {
	if credentials.PasswordHash == "" || credentials.SessionSecret == "" {
		return
	}
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	h.credentials = credentials
	h.credentialsExpiry = h.currentTime().Add(adminCredentialsTTL)
}

func (h *Handler) validPasswordWithCredentials(credentials Credentials, password string) bool {
	passwordHash := credentials.PasswordHash
	if passwordHash == "" {
		passwordHash = dummyBcryptHash
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
		return false
	}
	if credentials.SessionSecret == "" {
		return false
	}

	return true
}

func (h *Handler) authenticatedSession(ctx context.Context, request events.APIGatewayV2HTTPRequest) (adminSession, bool) {
	credentials := h.credentialsForRequest(ctx)
	if credentials.SessionSecret == "" {
		return adminSession{}, false
	}

	value, found := requestCookieValue(request, adminSessionCookieName)
	if !found {
		return adminSession{}, false
	}

	return decodeAdminSession(value, credentials.SessionSecret, h.currentTime())
}

func (h *Handler) validCSRF(ctx context.Context, request events.APIGatewayV2HTTPRequest, session adminSession) bool {
	credentials := h.credentialsForRequest(ctx)
	if credentials.SessionSecret == "" {
		return false
	}
	cookieValue, found := requestCookieValue(request, adminCSRFCookieName)
	if !found {
		return false
	}
	submittedValue := httpapi.HeaderValue(request.Headers, csrfHeaderName)
	if submittedValue == "" {
		values, err := httpapi.FormValues(request)
		if err != nil {
			return false
		}
		submittedValue = values.Get(csrfFieldName)
	}
	if submittedValue == "" || submittedValue != cookieValue {
		return false
	}

	return validateAdminCSRFToken(submittedValue, session, credentials.SessionSecret, h.currentTime())
}

func (h *Handler) csrfForProtectedResponse(ctx context.Context, request events.APIGatewayV2HTTPRequest, session adminSession) (string, []string, error) {
	credentials := h.credentialsForRequest(ctx)
	if csrfValue, found := requestCookieValue(request, adminCSRFCookieName); found {
		if validateAdminCSRFToken(csrfValue, session, credentials.SessionSecret, h.currentTime()) {
			return csrfValue, nil, nil
		}
	}

	csrfValue, err := newAdminCSRFToken(session, credentials.SessionSecret)
	if err != nil {
		return "", nil, err
	}
	return csrfValue, []string{adminCSRFCookie(csrfValue, session.ExpiresAt).String()}, nil
}

func (h *Handler) currentTime() time.Time {
	if h.now == nil {
		return time.Now()
	}
	return h.now()
}

func (h *Handler) handleProductImageUpload(ctx context.Context, path string, request events.APIGatewayV2HTTPRequest, session adminSession) events.APIGatewayV2HTTPResponse {
	step := productImageUploadStep(path)
	uploads, err := h.productImageUploads(ctx)
	if err != nil {
		logAdminError("product image upload: init service", err)
		h.recordProductImageUpload(step, metricOutcomeError)
		return adminJSONErrorResponse(http.StatusInternalServerError, "upload service unavailable")
	}

	switch path {
	case productImagePresignPath:
		var payload productImagePresignRequest
		if err := httpapi.JSONBody(request, &payload); err != nil {
			h.recordProductImageUpload(step, metricOutcomeValidationError)
			return adminJSONErrorResponse(http.StatusBadRequest, "invalid upload request")
		}
		if err := validateProductImagePresignRequest(payload); err != nil {
			h.recordProductImageUpload(step, metricOutcomeValidationError)
			return adminJSONErrorResponse(http.StatusBadRequest, "invalid upload request")
		}
		response, err := uploads.Presign(ctx, payload, h.currentTime())
		if err != nil {
			if isInvalidProductImageUpload(err) {
				h.recordProductImageUpload(step, metricOutcomeValidationError)
				return adminJSONErrorResponse(http.StatusBadRequest, "invalid upload request")
			}
			logAdminError("product image upload: presign", err)
			h.recordProductImageUpload(step, metricOutcomeError)
			return adminJSONErrorResponse(http.StatusInternalServerError, "upload service unavailable")
		}
		h.recordProductImageUpload(step, metricOutcomeSuccess)
		return adminJSONResponse(http.StatusOK, response)
	case productImageConfirmPath:
		var payload productImageConfirmRequest
		if err := httpapi.JSONBody(request, &payload); err != nil {
			h.recordProductImageUpload(step, metricOutcomeValidationError)
			return adminJSONErrorResponse(http.StatusBadRequest, "invalid upload request")
		}
		url, err := uploads.Confirm(ctx, payload)
		if err != nil {
			if isProductImageObjectNotFound(err) {
				h.recordProductImageUpload(step, metricOutcomeNotFound)
				return adminJSONErrorResponse(http.StatusNotFound, "uploaded object not found")
			}
			if isInvalidProductImageUpload(err) {
				h.recordProductImageUpload(step, metricOutcomeValidationError)
				return adminJSONErrorResponse(http.StatusBadRequest, "invalid upload request")
			}
			logAdminError("product image upload: confirm", err)
			h.recordProductImageUpload(step, metricOutcomeError)
			return adminJSONErrorResponse(http.StatusInternalServerError, "upload service unavailable")
		}
		credentials := h.credentialsForRequest(ctx)
		token, err := newConfirmedProductImageToken(url, session, credentials.SessionSecret, h.currentTime())
		if err != nil {
			logAdminError("product image upload: sign confirmed token", err)
			h.recordProductImageUpload(step, metricOutcomeError)
			return adminJSONErrorResponse(http.StatusInternalServerError, "upload service unavailable")
		}
		h.recordProductImageUpload(step, metricOutcomeSuccess)
		return adminJSONResponse(http.StatusOK, map[string]string{"url": url, "token": token})
	default:
		h.recordProductImageUpload(step, metricOutcomeNotFound)
		return adminJSONErrorResponse(http.StatusNotFound, "not found")
	}
}

func (h *Handler) productImageUploads(ctx context.Context) (productImageUploads, error) {
	h.stateMu.RLock()
	uploads := h.uploads
	h.stateMu.RUnlock()
	if uploads != nil {
		return uploads, nil
	}

	uploads, err := productImageUploadServiceFromEnvironment(ctx)
	if err != nil {
		return nil, err
	}

	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.uploads != nil {
		return h.uploads, nil
	}
	h.uploads = uploads
	return uploads, nil
}

func (h *Handler) loginThrottleForRequest() adminLoginThrottle {
	if h.loginThrottle == nil {
		return noopAdminLoginThrottle{}
	}
	return h.loginThrottle
}

func adminLoginThrottleResponse(status adminLoginThrottleStatus, now time.Time) events.APIGatewayV2HTTPResponse {
	return adminHTMLResponse(http.StatusTooManyRequests, "Too many login attempts. Try again later.", map[string]string{
		"Retry-After": strconv.Itoa(status.RetryAfter(now)),
	}, nil)
}

func loginPageBody(errorMessage string) string {
	errorHTML := ""
	if errorMessage != "" {
		errorHTML = `<p role="alert" class="mt-5 border-l-4 border-flag-red bg-flag-red/10 px-4 py-3 text-sm font-semibold text-flag-red">` + errorMessage + `</p>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="robots" content="noindex,nofollow"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Admin login</title><link rel="stylesheet" href="` + staticassets.AppCSSPath + `"></head><body class="min-h-screen bg-paper text-ink antialiased"><main class="flex min-h-screen items-center justify-center px-5"><div class="w-full max-w-sm border-2 border-ink bg-paper"><div aria-hidden="true" class="grid h-1.5 w-full grid-rows-[1fr_1fr_2fr_1fr_1fr]"><span class="bg-flag-red"></span><span class="bg-flag-white"></span><span class="bg-flag-blue"></span><span class="bg-flag-white"></span><span class="bg-flag-red"></span></div><div class="p-8"><h1 class="font-display text-3xl font-bold tracking-tight text-flag-blue">Admin login</h1>` + errorHTML + `<form method="post" action="/admin/login" class="mt-6 grid gap-4"><label class="grid gap-2 text-xs font-medium text-muted">Password <input type="password" name="password" autocomplete="current-password" class="h-12 border border-flag-blue/25 bg-flag-white px-4 text-base font-medium text-ink transition focus:border-flag-blue focus:bg-paper"></label><button type="submit" class="inline-flex h-12 items-center justify-center bg-flag-red px-6 text-sm font-bold text-white transition hover:bg-flag-red-deep">Sign in</button></form></div></div></main></body></html>`
}

func (h *Handler) adminPageBody(ctx context.Context, csrfValue string) (string, error) {
	vm, err := h.dashboardViewModel(ctx, csrfValue)
	if err != nil {
		return "", err
	}
	return renderAdminDashboard(ctx, vm)
}

func requestCookieValue(request events.APIGatewayV2HTTPRequest, name string) (string, bool) {
	return httpapi.CookieValue(request, name)
}

func (h *Handler) validAdminOrigin(request events.APIGatewayV2HTTPRequest) bool {
	return httpapi.ValidOriginSecrets(request, h.originSecretDigests, h.originSecretSet)
}

func adminLoginClient(request events.APIGatewayV2HTTPRequest) string {
	if client := normalizedAdminClientAddress(httpapi.HeaderValue(request.Headers, cloudFrontViewerAddressHeaderName)); client != "" {
		return client
	}
	sourceIP := strings.TrimSpace(request.RequestContext.HTTP.SourceIP)
	if sourceIP == "" {
		return unknownAdminLoginClient
	}
	if client := normalizedAdminClientAddress(sourceIP); client != "" {
		return client
	}
	return sourceIP
}

func normalizedAdminClientAddress(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		host = strings.Trim(host, "[]")
		if host != "" {
			return host
		}
	}
	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return ip.String()
	}
	if host, _, found := strings.Cut(value, ":"); found {
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			return ip.String()
		}
	}
	if split := strings.LastIndex(value, ":"); split > 0 {
		if ip := net.ParseIP(strings.Trim(value[:split], "[]")); ip != nil {
			return ip.String()
		}
	}
	return ""
}

func adminRedirectResponse(statusCode int, location string, cookies []string) events.APIGatewayV2HTTPResponse {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Cache-Control": "no-store",
			"Location":      location,
			"X-Robots-Tag":  adminRobotsTag,
		},
		Cookies: cookies,
	}
}

func adminHTMLResponse(statusCode int, body string, headers map[string]string, cookies []string) events.APIGatewayV2HTTPResponse {
	adminHeaders := map[string]string{"Cache-Control": "no-store", "X-Robots-Tag": adminRobotsTag}
	maps.Copy(adminHeaders, headers)
	return httpapi.HTMLResponseWithCookies(statusCode, body, adminHeaders, cookies)
}

func adminJSONErrorResponse(statusCode int, message string) events.APIGatewayV2HTTPResponse {
	return adminJSONResponse(statusCode, map[string]string{"error": message})
}

func adminJSONResponse(statusCode int, payload any) events.APIGatewayV2HTTPResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		statusCode = http.StatusInternalServerError
		body = []byte(`{"error":"internal server error"}`)
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Cache-Control": "no-store",
			"Content-Type":  httpapi.JSONContentType,
			"X-Robots-Tag":  adminRobotsTag,
		},
		Body: string(body),
	}
}
