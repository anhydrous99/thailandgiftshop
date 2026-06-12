package ssr

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
	"github.com/aws/aws-lambda-go/events"
	"golang.org/x/crypto/bcrypt"
)

// customerPasswordBcryptCost mirrors scripts/generate-admin-secret.go; tests
// override Handler.passwordHashCost with bcrypt.MinCost fixtures.
const customerPasswordBcryptCost = 12

const minPasswordBytes = 8
const maxPasswordBytes = 72
const maxEmailLength = 254

// dummyCustomerBcryptHash equalizes sign-in timing for unknown emails (the
// admin login precedent — never a real credential). It must carry the same
// cost as production hashes (customerPasswordBcryptCost) or the cheaper
// comparison itself becomes an email-existence timing oracle. Generated once
// via bcrypt.GenerateFromPassword([]byte("thailandgiftshop-dummy-timing-equalizer"), 12).
const dummyCustomerBcryptHash = "$2a$12$zgITMYyNspbQb4IWi/tW2OzM6bz92aTHL5sFMxg0R6ZnKP4R.gE/a"

// cartMergeAttempts caps version-conflict retries while merging the anonymous
// cookie cart into the server cart on login.
const cartMergeAttempts = 3

// createCustomerAttempts caps retries when CreateCustomer reports transient
// transaction contention (commerce.ErrTransientConflict). The classification
// is store-agnostic by contract even though only the Dynamo store produces it
// today, so the retry lives here in the caller.
const createCustomerAttempts = 3

const genericSignInError = "Invalid email or password."
const emailTakenError = "An account with this email already exists. Sign in instead."
const invalidEmailError = "Enter a valid email address."
const invalidPasswordError = "Use a password between 8 and 72 characters."
const expiredFormError = "This form expired. Please try again."
const throttledBody = "Too many attempts. Try again later."
const passwordResetSentCopy = "If an account uses that email, we sent a reset link."
const passwordResetInvalidCopy = "This reset link is invalid or expired."

const (
	authOperationSignUp         = "sign_up"
	authOperationSignIn         = "sign_in"
	authOperationPasswordChange = "password_change"
	authOperationPasswordReset  = "password_reset"

	authOutcomeSuccess   = "success"
	authOutcomeInvalid   = "invalid"
	authOutcomeThrottled = "throttled"
	authOutcomeError     = "error"
)

// handleAccountRoute dispatches every /account* route. The shared method gate
// and trailing-slash redirects already ran in handle(); session resolution
// and, for POSTs, CSRF validation happen here before any per-route work.
func (h *Handler) handleAccountRoute(ctx context.Context, request events.APIGatewayV2HTTPRequest, route pageRoute) events.APIGatewayV2HTTPResponse {
	switch route.kind {
	case pageAccountSignUp:
		return h.handleSignUpRoute(ctx, request)
	case pageAccountSignIn:
		return h.handleSignInRoute(ctx, request)
	case pageAccountPasswordReset:
		return h.handlePasswordResetRequestRoute(ctx, request)
	case pageAccountPasswordResetConfirm:
		return h.handlePasswordResetConfirmRoute(ctx, request)
	case pageAccountSignOut:
		return h.handleSignOut(ctx, request)
	}

	method := httpapi.Method(request)
	session, customer, signedIn, clearingCookies := h.customerSession(ctx, request)
	if !signedIn {
		return accountSeeOther(signInLocationForReturnTo(accountReturnToForRoute(route)), route.kind, clearingCookies)
	}
	if method == http.MethodPost && !h.validCustomerCSRF(request, session) {
		return accountHTMLResponse(http.StatusForbidden, "Forbidden", route.kind, nil)
	}

	switch route.kind {
	case pageAccount:
		return h.renderAccountPage(ctx, request, session, customer, accountPageState{
			PasswordChanged: request.QueryStringParameters["password_changed"] == "1",
		}, http.StatusOK)
	case pageAccountPassword:
		return h.handlePasswordChange(ctx, request, session, customer)
	case pageAccountAddresses:
		if method == http.MethodPost {
			return h.handleAddressCreate(ctx, request, session, customer)
		}
		return h.renderAddressesPage(ctx, request, session, customer, addressFormData{}, http.StatusOK)
	case pageAccountAddressEdit:
		return h.handleAddressEditPage(ctx, request, session, customer, route.slug)
	case pageAccountAddressUpdate:
		return h.handleAddressUpdate(ctx, request, session, customer, route.slug)
	case pageAccountAddressRemove:
		return h.handleAddressRemove(ctx, request, customer, route.slug)
	case pageAccountAddressDefault:
		return h.handleAddressDefault(ctx, request, customer, route.slug)
	case pageAccountPaymentMethods:
		return h.renderPaymentMethodsPage(ctx, request, session, customer, http.StatusOK)
	case pageAccountPaymentMethodAdd:
		return h.handlePaymentMethodAdd(ctx, customer)
	case pageAccountPaymentMethodRemove:
		return h.handlePaymentMethodRemove(ctx, customer, route.slug)
	}

	return h.accountNotFoundResponse(ctx, request, route.kind)
}

// accountReturnToForRoute picks the page a shopper lands back on after
// signing in: GET pages return to themselves, mutations to their parent page.
func accountReturnToForRoute(route pageRoute) string {
	switch route.kind {
	case pageAccountPassword:
		return "/account"
	case pageAccountAddressUpdate, pageAccountAddressRemove, pageAccountAddressDefault:
		return "/account/addresses"
	case pageAccountPaymentMethodAdd, pageAccountPaymentMethodRemove:
		return "/account/payment-methods"
	case pageAccountAddressEdit:
		return "/account/addresses/" + route.slug + "/edit"
	case pageAccountAddresses:
		return "/account/addresses"
	case pageAccountPaymentMethods:
		return "/account/payment-methods"
	}

	return "/account"
}

func (h *Handler) handleSignUpRoute(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	if _, _, signedIn, _ := h.customerSession(ctx, request); signedIn {
		return accountSeeOther("/account", pageAccountSignUp, nil)
	}
	if method != http.MethodPost {
		return h.signUpPageResponse(ctx, request, http.StatusOK, signUpPageData{
			ReturnTo: sanitizedReturnTo(request.QueryStringParameters[returnToFieldName], ""),
		}, "")
	}

	return h.handleSignUpSubmit(ctx, request)
}

func (h *Handler) handleSignUpSubmit(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageAccountSignUp, nil)
	}
	email := strings.TrimSpace(form.Get("email"))
	returnTo := sanitizedReturnTo(form.Get(returnToFieldName), "/account")
	rerender := signUpPageData{Email: email, ReturnTo: sanitizedReturnTo(form.Get(returnToFieldName), "")}

	if !h.validGuestCSRF(request) {
		rerender.ErrorMessage = expiredFormError
		return h.signUpPageResponse(ctx, request, http.StatusForbidden, rerender, "")
	}
	if h.commerce == nil {
		logAccountError("sign up", errors.New("commerce store not configured"))
		h.recordCustomerAuth(authOperationSignUp, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignUp, nil)
	}

	now := h.currentTime()
	ipKey := commerce.ThrottleKey(commerce.ThrottleScopeIP, customerLoginClient(request), h.customerSessionSecret)
	decision, err := h.commerce.ReserveLoginAttempt(ctx, ipKey, now)
	if err != nil {
		logAccountError("sign up: reserve throttle attempt", err)
		h.recordCustomerAuth(authOperationSignUp, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignUp, nil)
	}
	if !decision.Allowed {
		h.recordCustomerAuth(authOperationSignUp, authOutcomeThrottled)
		return customerThrottleResponse(pageAccountSignUp, now, decision)
	}

	guestToken := form.Get(guestCSRFFieldName)
	if !validCustomerEmail(email) {
		rerender.ErrorMessage = invalidEmailError
		return h.signUpPageResponse(ctx, request, http.StatusBadRequest, rerender, guestToken)
	}
	password := form.Get("password")
	if !validCustomerPassword(password) {
		rerender.ErrorMessage = invalidPasswordError
		return h.signUpPageResponse(ctx, request, http.StatusBadRequest, rerender, guestToken)
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), h.passwordCost())
	if err != nil {
		logAccountError("sign up: hash password", err)
		h.recordCustomerAuth(authOperationSignUp, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignUp, nil)
	}
	customer, err := h.createCustomerRetryingTransientConflicts(ctx, email, string(passwordHash))
	if errors.Is(err, commerce.ErrEmailTaken) {
		// Accepted, rate-limited enumeration trade-off: without email
		// infrastructure there is no "we emailed you a link" alternative.
		h.recordCustomerAuth(authOperationSignUp, authOutcomeInvalid)
		rerender.ErrorMessage = emailTakenError
		return h.signUpPageResponse(ctx, request, http.StatusBadRequest, rerender, guestToken)
	}
	if err != nil {
		logAccountError("sign up: create customer", err)
		h.recordCustomerAuth(authOperationSignUp, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignUp, nil)
	}

	// Clear-on-success, the admin precedent: a successful sign-up must not
	// leave the shared IP window counting toward a lockout.
	if err := h.commerce.ClearLoginAttempts(ctx, []string{ipKey}); err != nil {
		logAccountError("sign up: clear throttle", err)
		h.recordCustomerAuth(authOperationSignUp, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignUp, nil)
	}

	return h.finishCustomerAuth(ctx, request, pageAccountSignUp, authOperationSignUp, customer.ID, returnTo)
}

// createCustomerRetryingTransientConflicts re-attempts CreateCustomer when the
// store reports retryable transaction contention. Condition failures such as
// ErrEmailTaken keep their classification and are never retried.
func (h *Handler) createCustomerRetryingTransientConflicts(ctx context.Context, email string, passwordHash string) (commerce.Customer, error) {
	var customer commerce.Customer
	var err error
	for attempt := 0; attempt < createCustomerAttempts; attempt++ {
		customer, err = h.commerce.CreateCustomer(ctx, email, commerce.NormalizeEmail(email), passwordHash)
		if !errors.Is(err, commerce.ErrTransientConflict) {
			return customer, err
		}
	}

	return customer, err
}

func (h *Handler) handleSignInRoute(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	if _, _, signedIn, _ := h.customerSession(ctx, request); signedIn {
		return accountSeeOther("/account", pageAccountSignIn, nil)
	}
	if method != http.MethodPost {
		return h.signInPageResponse(ctx, request, http.StatusOK, signInPageData{
			ReturnTo:       sanitizedReturnTo(request.QueryStringParameters[returnToFieldName], ""),
			SuccessMessage: signInSuccessMessage(request),
		}, "")
	}

	return h.handleSignInSubmit(ctx, request)
}

func signInSuccessMessage(request events.APIGatewayV2HTTPRequest) string {
	if request.QueryStringParameters["password_reset"] == "1" {
		return "Password reset. Sign in with your new password."
	}
	return ""
}

func (h *Handler) handleSignInSubmit(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageAccountSignIn, nil)
	}
	email := strings.TrimSpace(form.Get("email"))
	returnTo := sanitizedReturnTo(form.Get(returnToFieldName), "/account")
	rerender := signInPageData{Email: email, ReturnTo: sanitizedReturnTo(form.Get(returnToFieldName), "")}

	if !h.validGuestCSRF(request) {
		rerender.ErrorMessage = expiredFormError
		return h.signInPageResponse(ctx, request, http.StatusForbidden, rerender, "")
	}
	if h.commerce == nil {
		logAccountError("sign in", errors.New("commerce store not configured"))
		h.recordCustomerAuth(authOperationSignIn, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignIn, nil)
	}

	// Reserve-before-verify on both hashed keys; either one locked rejects
	// the attempt with a generic body.
	now := h.currentTime()
	ipKey := commerce.ThrottleKey(commerce.ThrottleScopeIP, customerLoginClient(request), h.customerSessionSecret)
	emailKey := commerce.ThrottleKey(commerce.ThrottleScopeEmail, commerce.NormalizeEmail(email), h.customerSessionSecret)
	ipDecision, err := h.commerce.ReserveLoginAttempt(ctx, ipKey, now)
	if err != nil {
		logAccountError("sign in: reserve ip throttle attempt", err)
		h.recordCustomerAuth(authOperationSignIn, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignIn, nil)
	}
	emailDecision, err := h.commerce.ReserveLoginAttempt(ctx, emailKey, now)
	if err != nil {
		logAccountError("sign in: reserve email throttle attempt", err)
		h.recordCustomerAuth(authOperationSignIn, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignIn, nil)
	}
	if !ipDecision.Allowed || !emailDecision.Allowed {
		h.recordCustomerAuth(authOperationSignIn, authOutcomeThrottled)
		return customerThrottleResponse(pageAccountSignIn, now, ipDecision, emailDecision)
	}

	guestToken := form.Get(guestCSRFFieldName)
	password := form.Get("password")
	customer, found, err := h.commerce.GetCustomerByEmail(ctx, commerce.NormalizeEmail(email))
	if err != nil {
		logAccountError("sign in: lookup customer", err)
		h.recordCustomerAuth(authOperationSignIn, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignIn, nil)
	}
	if !found {
		// Burn a bcrypt comparison so unknown emails take as long as wrong
		// passwords (enumeration defense).
		_ = bcrypt.CompareHashAndPassword([]byte(dummyCustomerBcryptHash), []byte(password))
		h.recordCustomerAuth(authOperationSignIn, authOutcomeInvalid)
		rerender.ErrorMessage = genericSignInError
		return h.signInPageResponse(ctx, request, http.StatusUnauthorized, rerender, guestToken)
	}
	if bcrypt.CompareHashAndPassword([]byte(customer.PasswordHash), []byte(password)) != nil {
		h.recordCustomerAuth(authOperationSignIn, authOutcomeInvalid)
		rerender.ErrorMessage = genericSignInError
		return h.signInPageResponse(ctx, request, http.StatusUnauthorized, rerender, guestToken)
	}

	// A Clear failure blocks session issuance (the admin precedent): the
	// throttle window must never silently stop counting.
	if err := h.commerce.ClearLoginAttempts(ctx, []string{ipKey, emailKey}); err != nil {
		logAccountError("sign in: clear throttle", err)
		h.recordCustomerAuth(authOperationSignIn, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignIn, nil)
	}

	return h.finishCustomerAuth(ctx, request, pageAccountSignIn, authOperationSignIn, customer.ID, returnTo)
}

func (h *Handler) handlePasswordResetRequestRoute(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	if method != http.MethodPost {
		return h.passwordResetRequestPageResponse(ctx, request, http.StatusOK, passwordResetRequestPageData{
			Sent: request.QueryStringParameters["sent"] == "1",
		}, "")
	}
	return h.handlePasswordResetRequestSubmit(ctx, request)
}

func (h *Handler) handlePasswordResetRequestSubmit(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageAccountPasswordReset, nil)
	}
	emailAddress := strings.TrimSpace(form.Get("email"))
	rerender := passwordResetRequestPageData{Email: emailAddress}
	if !h.validGuestCSRF(request) {
		rerender.ErrorMessage = expiredFormError
		return h.passwordResetRequestPageResponse(ctx, request, http.StatusForbidden, rerender, "")
	}
	guestToken := form.Get(guestCSRFFieldName)
	if !validCustomerEmail(emailAddress) {
		rerender.ErrorMessage = invalidEmailError
		return h.passwordResetRequestPageResponse(ctx, request, http.StatusBadRequest, rerender, guestToken)
	}
	if h.commerce == nil || h.emailSender == nil {
		logAccountError("password reset: dependencies", errors.New("commerce store or email sender not configured"))
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordReset, nil)
	}

	now := h.currentTime().UTC()
	normalizedEmail := commerce.NormalizeEmail(emailAddress)
	ipKey := commerce.ThrottleKey("PASSWORD_RESET_IP", customerLoginClient(request), h.customerSessionSecret)
	emailKey := commerce.ThrottleKey("PASSWORD_RESET_EMAIL", normalizedEmail, h.customerSessionSecret)
	ipDecision, err := h.commerce.ReserveLoginAttempt(ctx, ipKey, now)
	if err != nil {
		logAccountError("password reset: reserve ip throttle attempt", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordReset, nil)
	}
	emailDecision, err := h.commerce.ReserveLoginAttempt(ctx, emailKey, now)
	if err != nil {
		logAccountError("password reset: reserve email throttle attempt", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordReset, nil)
	}
	if !ipDecision.Allowed || !emailDecision.Allowed {
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeThrottled)
		return customerThrottleResponse(pageAccountPasswordReset, now, ipDecision, emailDecision)
	}

	customer, found, err := h.commerce.GetCustomerByEmail(ctx, normalizedEmail)
	if err != nil {
		logAccountError("password reset: lookup customer", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordReset, nil)
	}
	if found {
		if err := h.issuePasswordResetEmail(ctx, customer, now); err != nil {
			logAccountError("password reset: issue email", err)
			h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
			return accountSeeOther("/account/password-reset?sent=1", pageAccountPasswordReset, nil)
		}
	}

	h.recordCustomerAuth(authOperationPasswordReset, authOutcomeSuccess)
	return accountSeeOther("/account/password-reset?sent=1", pageAccountPasswordReset, nil)
}

func (h *Handler) issuePasswordResetEmail(ctx context.Context, customer commerce.Customer, now time.Time) error {
	rawToken, err := signedtoken.RandomToken(32)
	if err != nil {
		return err
	}
	expiresAt := now.UTC().Add(passwordResetTTL)
	signedValue, err := signedtoken.Encode(passwordResetTokenPayload{
		Version:    customerSignedValueVersion,
		CustomerID: customer.ID,
		Token:      rawToken,
		ExpiresAt:  expiresAt.Unix(),
	}, h.customerSessionSecret, customerPasswordResetPurpose)
	if err != nil {
		return err
	}
	if err := h.commerce.PutPasswordResetToken(ctx, commerce.PasswordResetToken{
		CustomerID: customer.ID,
		TokenHash:  hashPasswordResetToken(rawToken),
		CreatedAt:  now.UTC(),
		ExpiresAt:  expiresAt,
		Version:    1,
	}); err != nil {
		return err
	}

	resetLink := h.resetBaseURL() + "/account/password-reset/confirm?token=" + url.QueryEscape(signedValue)
	_, err = h.emailSender.Send(ctx, email.BuildPasswordReset(customer.Email, resetLink))
	return err
}

func (h *Handler) handlePasswordResetConfirmRoute(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	if httpapi.Method(request) == http.MethodPost {
		return h.handlePasswordResetConfirmSubmit(ctx, request)
	}
	tokenValue := strings.TrimSpace(request.QueryStringParameters["token"])
	if _, ok := h.validatePasswordResetToken(ctx, tokenValue); !ok {
		return h.passwordResetConfirmPageResponse(ctx, request, http.StatusBadRequest, passwordResetConfirmPageData{ErrorMessage: passwordResetInvalidCopy}, "")
	}
	return h.passwordResetConfirmPageResponse(ctx, request, http.StatusOK, passwordResetConfirmPageData{Token: tokenValue}, "")
}

func (h *Handler) handlePasswordResetConfirmSubmit(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageAccountPasswordResetConfirm, nil)
	}
	tokenValue := strings.TrimSpace(form.Get("token"))
	renderData := passwordResetConfirmPageData{Token: tokenValue}
	if !h.validGuestCSRF(request) {
		renderData.ErrorMessage = expiredFormError
		return h.passwordResetConfirmPageResponse(ctx, request, http.StatusForbidden, renderData, "")
	}
	guestToken := form.Get(guestCSRFFieldName)
	newPassword := form.Get("new_password")
	if !validCustomerPassword(newPassword) {
		renderData.ErrorMessage = invalidPasswordError
		return h.passwordResetConfirmPageResponse(ctx, request, http.StatusBadRequest, renderData, guestToken)
	}
	payload, ok := h.decodePasswordResetToken(tokenValue)
	if !ok || h.commerce == nil {
		renderData.ErrorMessage = passwordResetInvalidCopy
		return h.passwordResetConfirmPageResponse(ctx, request, http.StatusBadRequest, renderData, guestToken)
	}

	now := h.currentTime().UTC()
	if _, found, err := h.commerce.ConsumePasswordResetToken(ctx, payload.CustomerID, hashPasswordResetToken(payload.Token), now); err != nil {
		logAccountError("password reset: consume token", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordResetConfirm, nil)
	} else if !found {
		renderData.ErrorMessage = passwordResetInvalidCopy
		return h.passwordResetConfirmPageResponse(ctx, request, http.StatusBadRequest, renderData, guestToken)
	}

	customer, found, err := h.commerce.GetCustomerByID(ctx, payload.CustomerID)
	if err != nil {
		logAccountError("password reset: load customer", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordResetConfirm, nil)
	}
	if !found {
		renderData.ErrorMessage = passwordResetInvalidCopy
		return h.passwordResetConfirmPageResponse(ctx, request, http.StatusBadRequest, renderData, guestToken)
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), h.passwordCost())
	if err != nil {
		logAccountError("password reset: hash password", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordResetConfirm, nil)
	}
	if err := h.commerce.DeleteAllSessions(ctx, customer.ID); err != nil {
		logAccountError("password reset: revoke sessions", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordResetConfirm, nil)
	}
	if err := h.commerce.UpdatePassword(ctx, customer.ID, string(newHash), customer.Version); err != nil {
		if errors.Is(err, commerce.ErrVersionConflict) {
			renderData.ErrorMessage = "Your account changed in another window. Request a new reset link."
			return h.passwordResetConfirmPageResponse(ctx, request, http.StatusConflict, renderData, guestToken)
		}
		logAccountError("password reset: update password", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordResetConfirm, nil)
	}
	if err := h.commerce.DeletePasswordResetToken(ctx, customer.ID); err != nil {
		logAccountError("password reset: delete token", err)
		h.recordCustomerAuth(authOperationPasswordReset, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordResetConfirm, nil)
	}

	h.recordCustomerAuth(authOperationPasswordReset, authOutcomeSuccess)
	return accountSeeOther("/account/sign-in?password_reset=1", pageAccountPasswordResetConfirm, []string{
		clearCustomerSessionCookie().String(),
		clearCustomerCSRFCookie().String(),
	})
}

func (h *Handler) validatePasswordResetToken(ctx context.Context, tokenValue string) (passwordResetTokenPayload, bool) {
	payload, ok := h.decodePasswordResetToken(tokenValue)
	if !ok || h.commerce == nil {
		return passwordResetTokenPayload{}, false
	}
	if _, found, err := h.commerce.ValidatePasswordResetToken(ctx, payload.CustomerID, hashPasswordResetToken(payload.Token), h.currentTime().UTC()); err != nil || !found {
		if err != nil {
			logAccountError("password reset: validate token", err)
		}
		return passwordResetTokenPayload{}, false
	}
	return payload, true
}

func (h *Handler) decodePasswordResetToken(tokenValue string) (passwordResetTokenPayload, bool) {
	var payload passwordResetTokenPayload
	if !signedtoken.Decode(tokenValue, h.customerSessionSecret, customerPasswordResetPurpose, &payload) {
		return passwordResetTokenPayload{}, false
	}
	if payload.Version != customerSignedValueVersion || payload.CustomerID == "" || payload.Token == "" {
		return passwordResetTokenPayload{}, false
	}
	if !h.currentTime().UTC().Before(time.Unix(payload.ExpiresAt, 0)) {
		return passwordResetTokenPayload{}, false
	}
	return payload, true
}

func (h *Handler) resetBaseURL() string {
	if h.passwordResetBaseURL != "" {
		return h.passwordResetBaseURL
	}
	return passwordResetPublicBaseURLFromEnvironment()
}

// finishCustomerAuth runs the shared sign-in/sign-up tail: a fresh
// server-minted session, the cookie-cart merge, and the PRG redirect. The
// response carries up to five cookies: session, CSRF, guest-CSRF clear, the
// guest order-pointer clear (meaningless once signed in; any stranded guest
// pending order self-heals via the 30-minute expiry webhook), and the
// rewritten cart mirror.
func (h *Handler) finishCustomerAuth(ctx context.Context, request events.APIGatewayV2HTTPRequest, kind pageKind, operation string, customerID string, returnTo string) events.APIGatewayV2HTTPResponse {
	minted, err := h.mintCustomerSession(ctx, customerID)
	if err != nil {
		logAccountError(operation+": mint session", err)
		h.recordCustomerAuth(operation, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", kind, nil)
	}
	mirrorCookie, err := h.mergeCartOnLogin(ctx, request, customerID)
	if err != nil {
		logAccountError(operation+": merge cart", err)
		h.recordCustomerAuth(operation, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", kind, nil)
	}

	h.recordCustomerAuth(operation, authOutcomeSuccess)
	return accountSeeOther(returnTo, kind, []string{
		minted.sessionCookie,
		minted.csrfCookie,
		clearGuestCSRFCookie().String(),
		clearGuestOrderCookie().String(),
		mirrorCookie,
	})
}

// mergeCartOnLogin folds the anonymous cookie cart into the server cart
// (per-line max, idempotent under login replay), normalizes against the live
// catalog, persists with version-conflict retries, and returns the rewritten
// tgs_cart mirror cookie.
func (h *Handler) mergeCartOnLogin(ctx context.Context, request events.APIGatewayV2HTTPRequest, customerID string) (string, error) {
	cookieCart := cart.Empty()
	if cookieValue, found := cartCookieValue(request); found {
		decoded := cart.DecodeCookie(cookieValue, h.cartCookieSecret)
		// A mirror-flagged cookie is a dead echo of some previous server-cart
		// state (a leftover from a revoked session), not anonymous shopping:
		// merging it would resurrect carts already cleared by payment or
		// pruned on another device. Pre-flag cookies decode as Mirror=false
		// and keep merging as genuine anonymous carts.
		if !decoded.Mirror {
			cookieCart = decoded.Cart
		}
	}

	normalizedCart := cart.Empty()
	written := false
	for attempt := 0; attempt < cartMergeAttempts && !written; attempt++ {
		record, _, err := h.commerce.GetCart(ctx, customerID)
		if err != nil {
			return "", err
		}
		record.CustomerID = customerID
		serverCart, err := cart.New(record.Lines)
		if err != nil {
			logAccountError("merge cart: invalid server lines", err)
			serverCart = cart.Empty()
		}

		merged := cart.Merge(serverCart, cookieCart)
		normalizedCart, _, _, err = h.normalizeCart(ctx, merged)
		if err != nil {
			return "", err
		}
		record.Lines = normalizedCart.Lines()
		if _, err := h.commerce.PutCart(ctx, record); err != nil {
			if errors.Is(err, commerce.ErrVersionConflict) {
				continue
			}
			return "", err
		}
		written = true
	}
	if !written {
		return "", errors.New("merge cart: version conflict retries exhausted")
	}

	return h.mirrorCartCookie(normalizedCart, request), nil
}

func (h *Handler) handleSignOut(ctx context.Context, request events.APIGatewayV2HTTPRequest) events.APIGatewayV2HTTPResponse {
	session, _, signedIn, _ := h.customerSession(ctx, request)
	if !signedIn {
		// The session is already dead (expired, or revoked elsewhere by a
		// password change), so its CSRF token can never validate. Treating
		// this sign-out as an idempotent cookie-clearing no-op is CSRF-safe:
		// it changes no server state and only removes the caller's own
		// cookies. It must still scrub every customer cookie — including the
		// tgs_cart mirror — so the next visitor on a shared machine never
		// sees (or merges) the previous customer's cart.
		return accountSeeOther("/", pageAccountSignOut, []string{
			clearCustomerSessionCookie().String(),
			clearCustomerCSRFCookie().String(),
			clearCartCookie(request),
		})
	}
	if !h.validCustomerCSRF(request, session) {
		return accountHTMLResponse(http.StatusForbidden, "Forbidden", pageAccountSignOut, nil)
	}
	if err := h.commerce.DeleteSession(ctx, session.CustomerID, session.TokenHash); err != nil {
		logAccountError("sign out: delete session", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignOut, nil)
	}

	// Clearing the tgs_cart mirror too is shared-computer privacy: the next
	// visitor must not see the signed-out customer's cart contents.
	return accountSeeOther("/", pageAccountSignOut, []string{
		clearCustomerSessionCookie().String(),
		clearCustomerCSRFCookie().String(),
		clearCartCookie(request),
	})
}

func (h *Handler) handlePasswordChange(ctx context.Context, request events.APIGatewayV2HTTPRequest, session commerce.Session, customer commerce.Customer) events.APIGatewayV2HTTPResponse {
	form, err := httpapi.FormValues(request)
	if err != nil {
		return accountHTMLResponse(http.StatusBadRequest, "Bad request", pageAccountPassword, nil)
	}

	newPassword := form.Get("new_password")
	if !validCustomerPassword(newPassword) {
		return h.renderAccountPage(ctx, request, session, customer, accountPageState{PasswordError: invalidPasswordError}, http.StatusBadRequest)
	}
	if bcrypt.CompareHashAndPassword([]byte(customer.PasswordHash), []byte(form.Get("current_password"))) != nil {
		h.recordCustomerAuth(authOperationPasswordChange, authOutcomeInvalid)
		return h.renderAccountPage(ctx, request, session, customer, accountPageState{PasswordError: "Current password is incorrect."}, http.StatusBadRequest)
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), h.passwordCost())
	if err != nil {
		logAccountError("password change: hash password", err)
		h.recordCustomerAuth(authOperationPasswordChange, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPassword, nil)
	}

	// Revoke every session BEFORE committing the new password (a leaked
	// credential is the reason passwords get changed). Failing here fails
	// closed: the password is unchanged and the user can simply retry. The
	// reverse order would leave previously-issued sessions — including an
	// attacker's — alive after a confirmed password change, with no way to
	// re-trigger revocation because the retry re-verifies the old password.
	if err := h.commerce.DeleteAllSessions(ctx, customer.ID); err != nil {
		logAccountError("password change: revoke sessions", err)
		h.recordCustomerAuth(authOperationPasswordChange, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPassword, nil)
	}
	if err := h.commerce.DeletePasswordResetToken(ctx, customer.ID); err != nil {
		logAccountError("password change: delete reset token", err)
		h.recordCustomerAuth(authOperationPasswordChange, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPassword, nil)
	}
	if err := h.commerce.UpdatePassword(ctx, customer.ID, string(newHash), customer.Version); err != nil {
		if errors.Is(err, commerce.ErrVersionConflict) {
			return h.renderAccountPage(ctx, request, session, customer, accountPageState{PasswordError: "Your account changed in another window. Try again."}, http.StatusConflict)
		}
		logAccountError("password change: update password", err)
		h.recordCustomerAuth(authOperationPasswordChange, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPassword, nil)
	}

	// Keep this browser signed in on a fresh token.
	minted, err := h.mintCustomerSession(ctx, customer.ID)
	if err != nil {
		logAccountError("password change: mint session", err)
		h.recordCustomerAuth(authOperationPasswordChange, authOutcomeError)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPassword, nil)
	}

	h.recordCustomerAuth(authOperationPasswordChange, authOutcomeSuccess)
	return accountSeeOther("/account?password_changed=1", pageAccountPassword, []string{
		minted.sessionCookie,
		minted.csrfCookie,
	})
}

func (h *Handler) signUpPageResponse(ctx context.Context, request events.APIGatewayV2HTTPRequest, statusCode int, vm signUpPageData, guestToken string) events.APIGatewayV2HTTPResponse {
	cookies := []string(nil)
	if guestToken == "" {
		minted, cookie, err := h.mintGuestCSRF()
		if err != nil {
			logAccountError("sign up: mint guest csrf", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignUp, nil)
		}
		guestToken = minted
		cookies = []string{cookie}
	}
	vm.Metadata = signUpMetadata()
	vm.HeaderCartLabel = h.cartNavigation(request)
	vm.GuestCSRFToken = guestToken

	var body bytes.Buffer
	if err := signUpPage(vm).Render(ctx, &body); err != nil {
		logAccountError("sign up: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignUp, nil)
	}

	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageAccountSignUp, cookies)
}

func (h *Handler) signInPageResponse(ctx context.Context, request events.APIGatewayV2HTTPRequest, statusCode int, vm signInPageData, guestToken string) events.APIGatewayV2HTTPResponse {
	cookies := []string(nil)
	if guestToken == "" {
		minted, cookie, err := h.mintGuestCSRF()
		if err != nil {
			logAccountError("sign in: mint guest csrf", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignIn, nil)
		}
		guestToken = minted
		cookies = []string{cookie}
	}
	vm.Metadata = signInMetadata()
	vm.HeaderCartLabel = h.cartNavigation(request)
	vm.GuestCSRFToken = guestToken

	var body bytes.Buffer
	if err := signInPage(vm).Render(ctx, &body); err != nil {
		logAccountError("sign in: render", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountSignIn, nil)
	}

	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageAccountSignIn, cookies)
}

func (h *Handler) passwordResetRequestPageResponse(ctx context.Context, request events.APIGatewayV2HTTPRequest, statusCode int, vm passwordResetRequestPageData, guestToken string) events.APIGatewayV2HTTPResponse {
	cookies := []string(nil)
	if guestToken == "" {
		minted, cookie, err := h.mintGuestCSRF()
		if err != nil {
			logAccountError("password reset: mint guest csrf", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordReset, nil)
		}
		guestToken = minted
		cookies = []string{cookie}
	}
	vm.Metadata = passwordResetRequestMetadata()
	vm.HeaderCartLabel = h.cartNavigation(request)
	vm.GuestCSRFToken = guestToken

	var body bytes.Buffer
	if err := passwordResetRequestPage(vm).Render(ctx, &body); err != nil {
		logAccountError("password reset: render request", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordReset, nil)
	}

	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageAccountPasswordReset, cookies)
}

func (h *Handler) passwordResetConfirmPageResponse(ctx context.Context, request events.APIGatewayV2HTTPRequest, statusCode int, vm passwordResetConfirmPageData, guestToken string) events.APIGatewayV2HTTPResponse {
	cookies := []string(nil)
	if guestToken == "" {
		minted, cookie, err := h.mintGuestCSRF()
		if err != nil {
			logAccountError("password reset: mint confirm guest csrf", err)
			return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordResetConfirm, nil)
		}
		guestToken = minted
		cookies = []string{cookie}
	}
	vm.Metadata = passwordResetConfirmMetadata()
	vm.HeaderCartLabel = h.cartNavigation(request)
	vm.GuestCSRFToken = guestToken

	var body bytes.Buffer
	if err := passwordResetConfirmPage(vm).Render(ctx, &body); err != nil {
		logAccountError("password reset: render confirm", err)
		return accountHTMLResponse(http.StatusInternalServerError, "Internal server error", pageAccountPasswordResetConfirm, nil)
	}

	return accountHTMLResponse(statusCode, maybeEmptyBody(httpapi.Method(request), body.String()), pageAccountPasswordResetConfirm, cookies)
}

// customerThrottleResponse renders the generic 429 with the longest
// Retry-After across the rejecting throttle decisions (never under a second).
func customerThrottleResponse(kind pageKind, now time.Time, decisions ...commerce.ThrottleDecision) events.APIGatewayV2HTTPResponse {
	retryAfter := 1
	for _, decision := range decisions {
		if seconds := decision.RetryAfter(now); seconds > retryAfter {
			retryAfter = seconds
		}
	}

	headers := seoHeadersForRoute(kind)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Retry-After"] = strconv.Itoa(retryAfter)
	return httpapi.HTMLResponse(http.StatusTooManyRequests, throttledBody, headers)
}

func validCustomerEmail(email string) bool {
	if email == "" || len(email) > maxEmailLength {
		return false
	}
	if strings.ContainsAny(email, " \t\r\n") {
		return false
	}
	if strings.Count(email, "@") != 1 {
		return false
	}
	local, domain, _ := strings.Cut(email, "@")
	if local == "" || domain == "" {
		return false
	}

	return strings.Contains(domain, ".")
}

// validCustomerPassword bounds passwords in bytes: bcrypt ignores input past
// 72 bytes, so longer values are rejected outright rather than silently
// truncated.
func validCustomerPassword(password string) bool {
	return len(password) >= minPasswordBytes && len(password) <= maxPasswordBytes
}

func (h *Handler) passwordCost() int {
	if h.passwordHashCost > 0 {
		return h.passwordHashCost
	}
	return customerPasswordBcryptCost
}

func (h *Handler) recordCustomerAuth(operation string, outcome string) {
	if h.metrics == nil {
		return
	}
	h.metrics.Record(observability.Count(
		observability.MetricCustomerAuth,
		observability.Dim("Service", "ssr"),
		observability.Dim("Operation", operation),
		observability.Dim("Outcome", outcome),
	))
}

func accountHTMLResponse(statusCode int, body string, kind pageKind, cookies []string) events.APIGatewayV2HTTPResponse {
	return httpapi.HTMLResponseWithCookies(statusCode, body, seoHeadersForRoute(kind), cookies)
}

// accountNotFoundResponse renders the styled 404 for the account and checkout
// handlers, dropping the body for HEAD requests (RFC 9110: HEAD responses
// carry headers only).
func (h *Handler) accountNotFoundResponse(ctx context.Context, request events.APIGatewayV2HTTPRequest, kind pageKind) events.APIGatewayV2HTTPResponse {
	return accountHTMLResponse(http.StatusNotFound, maybeEmptyBody(httpapi.Method(request), notFoundBody(ctx, h.cartNavigation(request))), kind, nil)
}

func accountSeeOther(location string, kind pageKind, cookies []string) events.APIGatewayV2HTTPResponse {
	return httpapi.SeeOther(location, cookies, seoHeadersForRoute(kind))
}
