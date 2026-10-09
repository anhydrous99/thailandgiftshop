package ssr

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/aws/aws-lambda-go/events"
)

func TestUnrelatedCustomerUpdatePreservesSession(t *testing.T) {
	env := newAccountTestEnv(t)
	jar := testCookieJar{}
	signUpTestCustomer(t, env.handler, jar, "address@example.test", "original-password")
	_, customer, signedIn, _ := env.handler.customerSession(context.Background(), jarPageRequest(http.MethodGet, "/account", jar))
	if !signedIn {
		t.Fatal("initial session rejected")
	}
	if err := env.commerce.SetDefaultAddress(context.Background(), customer.ID, "address", customer.Version); err != nil {
		t.Fatal(err)
	}
	if _, _, signedIn, _ := env.handler.customerSession(context.Background(), jarPageRequest(http.MethodGet, "/account", jar)); !signedIn {
		t.Fatal("address update revoked session")
	}
}

// Pause after password verification, before a session can be persisted.
type pausedSessionStore struct {
	commerce.Store
	entered chan struct{}
	resume  chan struct{}
}

func (s *pausedSessionStore) PutSession(ctx context.Context, session commerce.Session) error {
	close(s.entered)
	<-s.resume
	return s.Store.PutSession(ctx, session)
}

func TestDelayedOldPasswordSignInCannotSurviveCredentialUpdate(t *testing.T) {
	for _, flow := range []string{"change", "reset"} {
		t.Run(flow, func(t *testing.T) {
			env := newAccountTestEnv(t)
			currentJar := testCookieJar{}
			signUpTestCustomer(t, env.handler, currentJar, "revision@example.test", "original-password")
			jar := testCookieJar{}
			token := guestCSRFTokenFor(t, env.handler, jar, "/account/sign-in")
			paused := &pausedSessionStore{Store: env.commerce, entered: make(chan struct{}), resume: make(chan struct{})}
			// Separate handler avoids changing dependencies under a running request.
			delayedHandler := *env.handler
			delayedHandler.commerce = paused
			result := make(chan events.APIGatewayV2HTTPResponse, 1)
			failure := make(chan error, 1)
			go func() {
				response, err := delayedHandler.Handle(context.Background(), jarFormPostRequest("/account/sign-in", url.Values{
					guestCSRFFieldName: {token}, "email": {"revision@example.test"}, "password": {"original-password"},
				}, jar))
				result <- response
				failure <- err
			}()
			<-paused.entered
			if flow == "change" {
				csrf := accountCSRFToken(t, env.handler, currentJar)
				response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/password", url.Values{
					customerCSRFFieldName: {csrf}, "current_password": {"original-password"}, "new_password": {"replacement-password"},
				}, currentJar))
				if err != nil || response.StatusCode != http.StatusSeeOther {
					close(paused.resume)
					t.Fatalf("password change: status %d, error %v", response.StatusCode, err)
				}
				currentJar.update(t, response)
			} else {
				submitPasswordResetRequest(t, env.handler, testCookieJar{}, "revision@example.test")
				resetToken := resetTokenFromLastMessage(t, env.email)
				resetJar := testCookieJar{}
				page := getPasswordResetConfirm(t, env.handler, resetToken, resetJar)
				resetJar.update(t, page)
				response, err := env.handler.Handle(context.Background(), jarFormPostRequest("/account/password-reset/confirm", url.Values{
					guestCSRFFieldName: {hiddenInputValue(t, page.Body, guestCSRFFieldName)}, "token": {resetToken}, "new_password": {"replacement-password"},
				}, resetJar))
				if err != nil || response.StatusCode != http.StatusSeeOther {
					close(paused.resume)
					t.Fatalf("reset: status %d, error %v", response.StatusCode, err)
				}
			}
			close(paused.resume)
			response := <-result
			if err := <-failure; err != nil {
				t.Fatal(err)
			}
			jar.update(t, response)
			if _, _, signedIn, _, err := env.handler.customerSessionWithStoreError(context.Background(), jarPageRequest(http.MethodGet, "/account", jar)); err != nil || signedIn {
				t.Fatalf("stale session accepted=%v, error=%v", signedIn, err)
			}
			freshJar := testCookieJar{}
			signInTestCustomer(t, env.handler, freshJar, "revision@example.test", "replacement-password")
			if _, _, signedIn, _, err := env.handler.customerSessionWithStoreError(context.Background(), jarPageRequest(http.MethodGet, "/account", freshJar)); err != nil || !signedIn {
				t.Fatalf("fresh session accepted=%v, error=%v", signedIn, err)
			}
		})
	}
}
