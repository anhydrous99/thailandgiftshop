package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-lambda-go/events"
	"golang.org/x/crypto/bcrypt"
)

const htmlContentType = "text/html; charset=utf-8"
const jsonContentType = "application/json; charset=utf-8"

const adminAllowedMethods = http.MethodGet + ", " + http.MethodHead
const loginAllowedMethods = http.MethodGet + ", " + http.MethodHead + ", " + http.MethodPost
const logoutAllowedMethods = http.MethodPost
const uploadAllowedMethods = http.MethodPost

const csrfHeaderName = "X-CSRF-Token"
const adminOriginHeaderName = "X-TGS-Origin-Secret"
const cloudFrontViewerAddressHeaderName = "CloudFront-Viewer-Address"
const passwordFieldName = "password"
const csrfFieldName = "csrf_token"

const dummyBcryptHash = "$2a$04$Vn0nSllZrX4bNwFaMifZAuS4xCZ9oE4DngJ02k8pYDz7zlXzpOfgK"

type Handler struct {
	credentials   Credentials
	now           func() time.Time
	catalog       catalog.AdminStore
	uploads       productImageUploads
	loginThrottle adminLoginThrottle
	metrics       observability.Recorder
}

var adminColdStartRecorded atomic.Bool

func NewHandler() *Handler {
	return &Handler{now: time.Now, loginThrottle: noopAdminLoginThrottle{}}
}

func NewHandlerWithCredentials(credentials Credentials) *Handler {
	return &Handler{credentials: credentials, now: time.Now}
}

func NewHandlerWithCredentialsAndCatalog(credentials Credentials, adminStore catalog.AdminStore) *Handler {
	handler := NewHandlerWithCredentials(credentials)
	handler.catalog = adminStore
	return handler
}

func NewLocalDemoHandler(credentials Credentials, adminStore catalog.AdminStore) *Handler {
	handler := NewHandlerWithCredentialsAndCatalog(credentials, adminStore)
	handler.uploads = localProductImageUploads{}
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
	handler := NewHandlerWithCredentialsAndCatalog(credentials, adminStore)
	handler.loginThrottle = loginThrottle
	handler.metrics = metrics
	return handler, nil
}

var defaultHandler = NewHandler()

func Handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return defaultHandler.Handle(ctx, request)
}

func (h *Handler) Handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	started := time.Now()
	method := requestMethod(request)
	route := h.adminMetricRoute(request)
	response, err := h.handle(ctx, request)
	h.recordRouteMetrics(started, route, method, response.StatusCode)
	return response, err
}

func (h *Handler) handle(ctx context.Context, request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	path := requestPath(request)
	if path != "/admin" && !strings.HasPrefix(path, "/admin/") {
		return htmlResponse(http.StatusNotFound, "Not found", nil), nil
	}
	if !validAdminOrigin(request) {
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
	path := requestPath(request)
	if path != "/admin" && !strings.HasPrefix(path, "/admin/") {
		return "not_found"
	}
	if !validAdminOrigin(request) {
		return "origin_rejected"
	}
	if path == "/admin/login" {
		return "login"
	}
	if path == "/admin/logout" {
		return "logout"
	}
	if isAdminProductPath(path) {
		return "products"
	}
	if isAdminCategoryPath(path) {
		return "categories"
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
	method := requestMethod(request)
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

	credentials := h.credentialsForRequest(ctx)
	client := adminLoginClient(request)
	status, err := h.loginThrottleForRequest().ReserveAttempt(ctx, client, h.currentTime())
	if err != nil {
		h.recordAdminLoginAttempt(metricOutcomeError)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if !status.Allowed {
		h.recordAdminLoginAttempt(metricOutcomeThrottled)
		return adminLoginThrottleResponse(status, h.currentTime())
	}

	values, err := formValues(request)
	if err != nil || !h.validPasswordWithCredentials(credentials, values.Get(passwordFieldName)) {
		if status.Locked(h.currentTime()) {
			h.recordAdminLoginAttempt(metricOutcomeThrottled)
			return adminLoginThrottleResponse(status, h.currentTime())
		}
		h.recordAdminLoginAttempt(metricOutcomeInvalid)
		return adminHTMLResponse(http.StatusUnauthorized, loginPageBody("Invalid credentials"), nil, nil)
	}
	if err := h.loginThrottleForRequest().Clear(ctx, client); err != nil {
		h.recordAdminLoginAttempt(metricOutcomeError)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}

	session, sessionValue, err := newAdminSession(credentials.SessionSecret, h.currentTime())
	if err != nil {
		h.recordAdminLoginAttempt(metricOutcomeError)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	csrfValue, err := newAdminCSRFToken(session, credentials.SessionSecret)
	if err != nil {
		h.recordAdminLoginAttempt(metricOutcomeError)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}

	h.recordAdminLoginAttempt(metricOutcomeSuccess)
	return adminRedirectResponse(http.StatusSeeOther, "/admin", []string{
		adminSessionCookie(sessionValue, session.ExpiresAt).String(),
		adminCSRFCookie(csrfValue, session.ExpiresAt).String(),
	})
}

func (h *Handler) handleLogout(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	if requestMethod(request) != http.MethodPost {
		return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": logoutAllowedMethods}, nil)
	}

	session, authenticated := h.authenticatedSession(ctx, request)
	if !authenticated {
		return adminRedirectResponse(http.StatusSeeOther, "/admin/login", nil)
	}
	if !h.validCSRF(request, session) {
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

	path := requestPath(request)
	method := requestMethod(request)
	if method == http.MethodPost && !h.validCSRF(request, session) {
		return adminHTMLResponse(http.StatusForbidden, "Forbidden", nil, nil)
	}
	if isAdminProductPath(path) {
		return h.handleAdminProducts(ctx, path, request, session)
	}
	if isAdminCategoryPath(path) {
		return h.handleAdminCategories(ctx, path, request, session)
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

	csrfValue, cookies, err := h.csrfForProtectedResponse(request, session)
	if err != nil {
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	body, err := h.adminPageBody(ctx, csrfValue)
	if err != nil {
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if method == http.MethodHead {
		body = ""
	}

	return adminHTMLResponse(http.StatusOK, body, nil, cookies)
}

func (h *Handler) validPassword(ctx context.Context, password string) bool {
	return h.validPasswordWithCredentials(h.credentialsForRequest(ctx), password)
}

func (h *Handler) credentialsForRequest(ctx context.Context) Credentials {
	credentials := h.credentials
	if credentials.PasswordHash == "" || credentials.SessionSecret == "" {
		loadedCredentials, err := CredentialsFromEnvironment(ctx)
		if err == nil {
			credentials = loadedCredentials
		}
	}
	if credentials.PasswordHash != "" || credentials.SessionSecret != "" {
		h.credentials = credentials
	}
	return credentials
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

	h.credentials = credentials
	return true
}

func (h *Handler) authenticatedSession(ctx context.Context, request events.APIGatewayV2HTTPRequest) (adminSession, bool) {
	credentials := h.credentials
	if credentials.SessionSecret == "" {
		loadedCredentials, err := CredentialsFromEnvironment(ctx)
		if err == nil {
			credentials = loadedCredentials
			h.credentials = loadedCredentials
		}
	}
	if credentials.SessionSecret == "" {
		return adminSession{}, false
	}

	value, found := requestCookieValue(request, adminSessionCookieName)
	if !found {
		return adminSession{}, false
	}

	return decodeAdminSession(value, credentials.SessionSecret, h.currentTime())
}

func (h *Handler) validCSRF(request events.APIGatewayV2HTTPRequest, session adminSession) bool {
	cookieValue, found := requestCookieValue(request, adminCSRFCookieName)
	if !found {
		return false
	}
	submittedValue := headerValue(request.Headers, csrfHeaderName)
	if submittedValue == "" {
		values, err := formValues(request)
		if err != nil {
			return false
		}
		submittedValue = values.Get(csrfFieldName)
	}
	if submittedValue == "" || submittedValue != cookieValue {
		return false
	}

	return validateAdminCSRFToken(submittedValue, session, h.credentials.SessionSecret, h.currentTime())
}

func (h *Handler) csrfForProtectedResponse(request events.APIGatewayV2HTTPRequest, session adminSession) (string, []string, error) {
	if csrfValue, found := requestCookieValue(request, adminCSRFCookieName); found {
		if validateAdminCSRFToken(csrfValue, session, h.credentials.SessionSecret, h.currentTime()) {
			return csrfValue, nil, nil
		}
	}

	csrfValue, err := newAdminCSRFToken(session, h.credentials.SessionSecret)
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
		h.recordProductImageUpload(step, metricOutcomeError)
		return adminJSONErrorResponse(http.StatusInternalServerError, "upload service unavailable")
	}

	switch path {
	case productImagePresignPath:
		var payload productImagePresignRequest
		if err := jsonRequestBody(request, &payload); err != nil {
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
			h.recordProductImageUpload(step, metricOutcomeError)
			return adminJSONErrorResponse(http.StatusInternalServerError, "upload service unavailable")
		}
		h.recordProductImageUpload(step, metricOutcomeSuccess)
		return adminJSONResponse(http.StatusOK, response)
	case productImageConfirmPath:
		var payload productImageConfirmRequest
		if err := jsonRequestBody(request, &payload); err != nil {
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
			h.recordProductImageUpload(step, metricOutcomeError)
			return adminJSONErrorResponse(http.StatusInternalServerError, "upload service unavailable")
		}
		token, err := newConfirmedProductImageToken(url, session, h.credentials.SessionSecret, h.currentTime())
		if err != nil {
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
	if h.uploads != nil {
		return h.uploads, nil
	}
	uploads, err := productImageUploadServiceFromEnvironment(ctx)
	if err != nil {
		return nil, err
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
		errorHTML = `<p role="alert">` + errorMessage + `</p>`
	}
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Admin login</title></head><body><h1>Admin login</h1>` + errorHTML + `<form method="post" action="/admin/login"><label>Password <input type="password" name="password" autocomplete="current-password"></label><button type="submit">Sign in</button></form></body></html>`
}

func (h *Handler) adminPageBody(ctx context.Context, csrfValue string) (string, error) {
	vm, err := h.dashboardViewModel(ctx, csrfValue)
	if err != nil {
		return "", err
	}
	return renderAdminDashboard(ctx, vm)
}

func requestPath(request events.APIGatewayV2HTTPRequest) string {
	if request.RawPath != "" {
		return request.RawPath
	}
	if request.RequestContext.HTTP.Path != "" {
		return request.RequestContext.HTTP.Path
	}

	return "/"
}

func requestMethod(request events.APIGatewayV2HTTPRequest) string {
	if request.RequestContext.HTTP.Method != "" {
		return request.RequestContext.HTTP.Method
	}

	return http.MethodGet
}

func requestCookieValue(request events.APIGatewayV2HTTPRequest, name string) (string, bool) {
	for _, cookieHeader := range request.Cookies {
		if value, found := namedCookieValue(cookieHeader, name); found {
			return value, true
		}
	}
	if cookieHeader := headerValue(request.Headers, "Cookie"); cookieHeader != "" {
		return namedCookieValue(cookieHeader, name)
	}

	return "", false
}

func namedCookieValue(cookieHeader string, name string) (string, bool) {
	for part := range strings.SplitSeq(cookieHeader, ";") {
		cookieName, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && cookieName == name {
			return value, true
		}
	}

	return "", false
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}

	return ""
}

func validAdminOrigin(request events.APIGatewayV2HTTPRequest) bool {
	secret := strings.TrimSpace(os.Getenv(EnvAdminOriginHeaderSecret))
	if secret == "" {
		return true
	}
	provided := headerValue(request.Headers, adminOriginHeaderName)
	providedDigest := sha256.Sum256([]byte(provided))
	secretDigest := sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(providedDigest[:], secretDigest[:]) == 1
}

func adminLoginClient(request events.APIGatewayV2HTTPRequest) string {
	if client := normalizedAdminClientAddress(headerValue(request.Headers, cloudFrontViewerAddressHeaderName)); client != "" {
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

func formValues(request events.APIGatewayV2HTTPRequest) (url.Values, error) {
	body := request.Body
	if request.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, err
		}
		body = string(decoded)
	}

	return url.ParseQuery(body)
}

func jsonRequestBody(request events.APIGatewayV2HTTPRequest, target any) error {
	body := request.Body
	if request.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return err
		}
		body = string(decoded)
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func adminRedirectResponse(statusCode int, location string, cookies []string) events.APIGatewayV2HTTPResponse {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Cache-Control": "no-store",
			"Location":      location,
		},
		Cookies: cookies,
	}
}

func htmlResponse(statusCode int, body string, headers map[string]string) events.APIGatewayV2HTTPResponse {
	return htmlResponseWithCookies(statusCode, body, headers, nil)
}

func adminHTMLResponse(statusCode int, body string, headers map[string]string, cookies []string) events.APIGatewayV2HTTPResponse {
	adminHeaders := map[string]string{"Cache-Control": "no-store"}
	maps.Copy(adminHeaders, headers)
	return htmlResponseWithCookies(statusCode, body, adminHeaders, cookies)
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
			"Content-Type":  jsonContentType,
		},
		Body: string(body),
	}
}

func htmlResponseWithCookies(statusCode int, body string, headers map[string]string, cookies []string) events.APIGatewayV2HTTPResponse {
	responseHeaders := map[string]string{
		"Content-Type": htmlContentType,
	}
	maps.Copy(responseHeaders, headers)

	return events.APIGatewayV2HTTPResponse{
		StatusCode: statusCode,
		Headers:    responseHeaders,
		Body:       body,
		Cookies:    cookies,
	}
}
