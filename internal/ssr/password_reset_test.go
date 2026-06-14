package ssr

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
	"github.com/aws/aws-lambda-go/events"
)

func TestPasswordResetRequestEnumerationSafeAndSendsKnownCustomer(t *testing.T) {
	env := newAccountTestEnv(t)
	t.Setenv("PUBLIC_BASE_URL", "https://configured.example")
	env.handler.passwordResetBaseURL = passwordResetPublicBaseURLFromEnvironment()
	knownEmail := "Reset.Shopper@Example.com"
	signUpTestCustomer(t, env.handler, testCookieJar{}, knownEmail, "orchid-market-99")
	env.email.Clear()

	known := submitPasswordResetRequest(t, env.handler, testCookieJar{}, knownEmail)
	unknown := submitPasswordResetRequest(t, env.handler, testCookieJar{}, "missing@example.com")

	if known.StatusCode != http.StatusSeeOther || unknown.StatusCode != http.StatusSeeOther {
		t.Fatalf("statuses = %d/%d, want both 303", known.StatusCode, unknown.StatusCode)
	}
	if known.Headers["Location"] != "/account/password-reset?sent=1" || unknown.Headers["Location"] != known.Headers["Location"] {
		t.Fatalf("locations = %q/%q, want identical reset sent redirect", known.Headers["Location"], unknown.Headers["Location"])
	}
	if known.Body != unknown.Body {
		t.Fatalf("bodies differ for known/unknown reset request: %q vs %q", known.Body, unknown.Body)
	}

	messages := env.email.Messages()
	if len(messages) != 1 {
		t.Fatalf("fake email count = %d, want 1", len(messages))
	}
	if messages[0].To != knownEmail || messages[0].Kind != email.MessageKindPasswordReset {
		t.Fatalf("message = %#v, want password reset to known email", messages[0])
	}
	resetLink := resetLinkFromMessage(t, messages[0])
	if !strings.HasPrefix(resetLink, "https://configured.example/account/password-reset/confirm?token=") {
		t.Fatalf("reset link = %q, want configured public base URL", resetLink)
	}
	if strings.Contains(resetLink, "evil.example") || strings.Contains(resetLink, "forwarded.example") {
		t.Fatalf("reset link used spoofed host data: %q", resetLink)
	}
}

func TestPasswordResetRequestAppliesConstantTimeFloorToKnownAndUnknown(t *testing.T) {
	env := newAccountTestEnv(t)
	const floor = 120 * time.Millisecond
	env.handler.passwordResetFloor = floor
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")
	env.email.Clear()

	knownStart := time.Now()
	submitPasswordResetRequest(t, env.handler, testCookieJar{}, "shopper@example.com")
	knownElapsed := time.Since(knownStart)

	unknownStart := time.Now()
	submitPasswordResetRequest(t, env.handler, testCookieJar{}, "missing@example.com")
	unknownElapsed := time.Since(unknownStart)

	if knownElapsed < floor {
		t.Errorf("known-email reset took %s, want at least the %s floor", knownElapsed, floor)
	}
	if unknownElapsed < floor {
		t.Errorf("unknown-email reset took %s, want at least the %s floor (no timing oracle)", unknownElapsed, floor)
	}
}

func TestPasswordResetRequestReissueInvalidatesOldLink(t *testing.T) {
	env := newAccountTestEnv(t)
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")

	submitPasswordResetRequest(t, env.handler, testCookieJar{}, "shopper@example.com")
	oldToken := resetTokenFromLastMessage(t, env.email)
	submitPasswordResetRequest(t, env.handler, testCookieJar{}, "shopper@example.com")
	newToken := resetTokenFromLastMessage(t, env.email)
	if oldToken == newToken {
		t.Fatal("reissued reset token matched old token")
	}

	oldResponse := getPasswordResetConfirm(t, env.handler, oldToken, testCookieJar{})
	if oldResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("old confirm GET status = %d, want 400", oldResponse.StatusCode)
	}
	newResponse := getPasswordResetConfirm(t, env.handler, newToken, testCookieJar{})
	if newResponse.StatusCode != http.StatusOK {
		t.Fatalf("new confirm GET status = %d body %q, want 200", newResponse.StatusCode, newResponse.Body)
	}
	assertBodyContains(t, newResponse.Body, []string{`data-testid="password-reset-confirm-form"`, `name="token"`})
}

func TestPasswordResetConfirmRejectsBadTokensSafely(t *testing.T) {
	env := newAccountTestEnv(t)
	signUpTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "orchid-market-99")
	customer, found, err := env.commerce.GetCustomerByEmail(context.Background(), commerce.NormalizeEmail("shopper@example.com"))
	if err != nil || !found {
		t.Fatalf("customer lookup = found %t err %v", found, err)
	}

	now := env.handler.currentTime().UTC()
	expiredRaw := "expired-reset-token"
	if err := env.commerce.PutPasswordResetToken(context.Background(), commerce.PasswordResetToken{CustomerID: customer.ID, TokenHash: hashPasswordResetToken(expiredRaw), CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute), Version: 1}); err != nil {
		t.Fatalf("PutPasswordResetToken expired: %v", err)
	}
	expired := signedPasswordResetTestToken(t, env.handler, customer.ID, expiredRaw, now.Add(-time.Minute), customerPasswordResetPurpose)

	wrongPurposeRaw := "wrong-purpose-reset-token"
	if err := env.commerce.PutPasswordResetToken(context.Background(), commerce.PasswordResetToken{CustomerID: customer.ID, TokenHash: hashPasswordResetToken(wrongPurposeRaw), CreatedAt: now, ExpiresAt: now.Add(passwordResetTTL), Version: 1}); err != nil {
		t.Fatalf("PutPasswordResetToken wrong purpose: %v", err)
	}
	wrongPurpose := signedPasswordResetTestToken(t, env.handler, customer.ID, wrongPurposeRaw, now.Add(passwordResetTTL), customerCSRFPurpose)

	mismatch := signedPasswordResetTestToken(t, env.handler, customer.ID, "not-the-stored-token", now.Add(passwordResetTTL), customerPasswordResetPurpose)

	for _, test := range []struct {
		name  string
		token string
	}{
		{name: "malformed", token: "not-a-token"},
		{name: "expired", token: expired},
		{name: "wrong purpose", token: wrongPurpose},
		{name: "mismatched hash", token: mismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := getPasswordResetConfirm(t, env.handler, test.token, testCookieJar{})
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body %q, want 400", response.StatusCode, response.Body)
			}
			assertBodyContains(t, response.Body, []string{"This reset link is invalid or expired."})
		})
	}
}

func TestPasswordResetConfirmSuccessChangesPasswordDeletesSessionsAndToken(t *testing.T) {
	env := newAccountTestEnv(t)
	firstJar := testCookieJar{}
	signUpTestCustomer(t, env.handler, firstJar, "shopper@example.com", "orchid-market-99")
	secondJar := testCookieJar{}
	signInTestCustomer(t, env.handler, secondJar, "shopper@example.com", "orchid-market-99")
	env.email.Clear()

	submitPasswordResetRequest(t, env.handler, testCookieJar{}, "shopper@example.com")
	token := resetTokenFromLastMessage(t, env.email)
	confirmJar := testCookieJar{}
	firstGet := getPasswordResetConfirm(t, env.handler, token, confirmJar)
	if firstGet.StatusCode != http.StatusOK {
		t.Fatalf("confirm GET status = %d body %q, want 200", firstGet.StatusCode, firstGet.Body)
	}
	confirmJar.update(t, firstGet)
	csrfToken := hiddenInputValue(t, firstGet.Body, guestCSRFFieldName)
	secondGet := getPasswordResetConfirm(t, env.handler, token, confirmJar)
	if secondGet.StatusCode != http.StatusOK {
		t.Fatalf("second confirm GET status = %d, want non-consuming 200", secondGet.StatusCode)
	}

	postResponse, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/password-reset/confirm", url.Values{
		guestCSRFFieldName: {csrfToken},
		"token":            {token},
		"new_password":     {"replacement-pw-55"},
	}, confirmJar))
	if err != nil {
		t.Fatalf("confirm POST Handle returned error: %v", err)
	}
	if postResponse.StatusCode != http.StatusSeeOther || postResponse.Headers["Location"] != "/account/sign-in?password_reset=1" {
		t.Fatalf("confirm POST = %d %q, want 303 sign-in success", postResponse.StatusCode, postResponse.Headers["Location"])
	}

	reused := getPasswordResetConfirm(t, env.handler, token, testCookieJar{})
	if reused.StatusCode != http.StatusBadRequest {
		t.Fatalf("reused token GET status = %d, want 400", reused.StatusCode)
	}
	customerID := accountCustomerID(t, env, "shopper@example.com")
	if _, found, err := env.commerce.ValidatePasswordResetToken(context.Background(), customerID, "unused", env.handler.currentTime()); err != nil || found {
		t.Fatalf("reset row after success found=%t err=%v, want deleted/missing", found, err)
	}

	oldSession, err := env.handler.Handle(context.Background(), jarPageRequest(http.MethodGet, "/account", firstJar))
	if err != nil {
		t.Fatalf("old session Handle returned error: %v", err)
	}
	if oldSession.StatusCode != http.StatusSeeOther || !strings.HasPrefix(oldSession.Headers["Location"], "/account/sign-in") {
		t.Fatalf("old session /account = %d %q, want sign-in redirect", oldSession.StatusCode, oldSession.Headers["Location"])
	}
	oldPasswordJar := testCookieJar{}
	oldPasswordToken := guestCSRFTokenFor(t, env.handler, oldPasswordJar, "/account/sign-in")
	oldPassword, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
		guestCSRFFieldName: {oldPasswordToken},
		"email":            {"shopper@example.com"},
		"password":         {"orchid-market-99"},
	}, oldPasswordJar))
	if err != nil {
		t.Fatalf("old password sign-in returned error: %v", err)
	}
	if oldPassword.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password sign-in = %d, want 401", oldPassword.StatusCode)
	}
	signInTestCustomer(t, env.handler, testCookieJar{}, "shopper@example.com", "replacement-pw-55")

	signInRequest := pageRequest(http.MethodGet, "/account/sign-in")
	signInRequest.QueryStringParameters = map[string]string{"password_reset": "1"}
	signInSuccess, err := env.handler.Handle(context.Background(), signInRequest)
	if err != nil {
		t.Fatalf("sign-in success render returned error: %v", err)
	}
	assertBodyContains(t, signInSuccess.Body, []string{"Password reset. Sign in with your new password."})

	_ = secondJar
}

func TestAdminPasswordResetRouteAbsent(t *testing.T) {
	response, err := Handle(context.Background(), pageRequest(http.MethodGet, "/admin/password-reset"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		t.Fatalf("admin password reset status = %d, want non-success", response.StatusCode)
	}
}

func submitPasswordResetRequest(t *testing.T, handler *Handler, jar testCookieJar, emailAddress string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	token := guestCSRFTokenFor(t, handler, jar, "/account/password-reset")
	request := jarFormPostRequest("/account/password-reset", url.Values{
		guestCSRFFieldName: {token},
		"email":            {emailAddress},
	}, jar)
	request.Headers["Host"] = "evil.example"
	request.Headers["X-Forwarded-Host"] = "forwarded.example"
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("password reset request Handle returned error: %v", err)
	}
	jar.update(t, response)
	return response
}

func getPasswordResetConfirm(t *testing.T, handler *Handler, token string, jar testCookieJar) events.APIGatewayV2HTTPResponse {
	t.Helper()
	request := jarPageRequest(http.MethodGet, "/account/password-reset/confirm", jar)
	request.RawQueryString = "token=" + url.QueryEscape(token)
	request.QueryStringParameters = map[string]string{"token": token}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("password reset confirm Handle returned error: %v", err)
	}
	return response
}

func resetTokenFromLastMessage(t *testing.T, sender *email.FakeSender) string {
	t.Helper()
	messages := sender.Messages()
	if len(messages) == 0 {
		t.Fatal("no password reset email captured")
	}
	link := resetLinkFromMessage(t, messages[len(messages)-1])
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse reset link %q: %v", link, err)
	}
	return parsed.Query().Get("token")
}

func resetLinkFromMessage(t *testing.T, message email.Message) string {
	t.Helper()
	for _, line := range strings.Split(message.Text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return line
		}
	}
	t.Fatalf("reset link not found in message text %q", message.Text)
	return ""
}

func signedPasswordResetTestToken(t *testing.T, handler *Handler, customerID string, rawToken string, expiresAt time.Time, purpose []byte) string {
	t.Helper()
	value, err := signedtoken.Encode(passwordResetTokenPayload{
		Version:    customerSignedValueVersion,
		CustomerID: customerID,
		Token:      rawToken,
		ExpiresAt:  expiresAt.Unix(),
	}, handler.customerSessionSecret, purpose)
	if err != nil {
		t.Fatalf("Encode reset token: %v", err)
	}
	return value
}
