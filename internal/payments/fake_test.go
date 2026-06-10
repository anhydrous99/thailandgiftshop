package payments

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v82/webhook"
)

func testPaymentSessionInput(orderID string) PaymentSessionInput {
	return PaymentSessionInput{
		OrderID:          orderID,
		CustomerID:       "cus0000000000000000000000a",
		StripeCustomerID: "cus_fake_cus0000000000000000000000a",
		Email:            "shopper@example.test",
		Lines:            []SessionLine{{Name: "Mango Sticky Rice Candy", UnitAmountCents: 450, Quantity: 2}},
		TotalCents:       900,
		SuccessURL:       "http://127.0.0.1:8080/checkout/confirm?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:        "http://127.0.0.1:8080/checkout?canceled=1",
		ExpiresAt:        time.Date(2026, 6, 9, 12, 30, 0, 0, time.UTC),
		Attempt:          1,
	}
}

func TestFakeProviderEnsureCustomerIsDeterministic(t *testing.T) {
	provider := NewFakeProvider()

	first, err := provider.EnsureCustomer(context.Background(), "cus0000000000000000000000a", "shopper@example.test")
	if err != nil {
		t.Fatalf("EnsureCustomer returned error: %v", err)
	}
	second, err := provider.EnsureCustomer(context.Background(), "cus0000000000000000000000a", "other@example.test")
	if err != nil {
		t.Fatalf("EnsureCustomer returned error: %v", err)
	}
	if first != "cus_fake_cus0000000000000000000000a" || first != second {
		t.Fatalf("EnsureCustomer = %q then %q, want deterministic cus_fake_ prefix", first, second)
	}
}

func TestFakeProviderCreatePaymentSessionIsDeterministic(t *testing.T) {
	provider := NewFakeProvider()
	input := testPaymentSessionInput("ord0000000000000000000000a")

	session, err := provider.CreatePaymentSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}
	want := Session{
		ID:               "cs_fake_ord0000000000000000000000a",
		URL:              "/checkout/fake-pay?session_id=cs_fake_ord0000000000000000000000a",
		OrderID:          "ord0000000000000000000000a",
		PaymentStatus:    "unpaid",
		Status:           SessionStatusOpen,
		AmountTotalCents: 900,
		Mode:             "payment",
	}
	if session != want {
		t.Fatalf("session = %#v, want %#v", session, want)
	}

	retrieved, err := provider.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if retrieved != want {
		t.Fatalf("GetSession = %#v, want %#v", retrieved, want)
	}

	again, err := provider.CreatePaymentSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}
	if again != want {
		t.Fatalf("repeated CreatePaymentSession = %#v, want %#v", again, want)
	}
}

func TestFakeProviderGetSessionUnknownID(t *testing.T) {
	provider := NewFakeProvider()
	if _, err := provider.GetSession(context.Background(), "cs_fake_missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetSession error = %v, want %v", err, ErrSessionNotFound)
	}
}

func TestFakeProviderMarkSessionPaid(t *testing.T) {
	provider := NewFakeProvider()
	input := testPaymentSessionInput("ord0000000000000000000000a")
	session, err := provider.CreatePaymentSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}

	successURL, err := provider.MarkSessionPaid(session.ID, false)
	if err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}
	wantSuccessURL := "http://127.0.0.1:8080/checkout/confirm?session_id=cs_fake_ord0000000000000000000000a"
	if successURL != wantSuccessURL {
		t.Fatalf("successURL = %q, want %q", successURL, wantSuccessURL)
	}

	paid, err := provider.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if paid.PaymentStatus != "paid" {
		t.Fatalf("PaymentStatus = %q, want %q", paid.PaymentStatus, "paid")
	}
	if paid.PaymentIntentID != "pi_fake_ord0000000000000000000000a" {
		t.Fatalf("PaymentIntentID = %q, want %q", paid.PaymentIntentID, "pi_fake_ord0000000000000000000000a")
	}
	if paid.CardBrand != "visa" || paid.CardLast4 != "4242" {
		t.Fatalf("card = %q/%q, want visa/4242", paid.CardBrand, paid.CardLast4)
	}
	if paid.AmountTotalCents != 900 {
		t.Fatalf("AmountTotalCents = %d, want 900", paid.AmountTotalCents)
	}
	if paid.Status != SessionStatusComplete {
		t.Fatalf("Status = %q, want %q", paid.Status, SessionStatusComplete)
	}

	methods, err := provider.ListPaymentMethods(context.Background(), input.StripeCustomerID)
	if err != nil {
		t.Fatalf("ListPaymentMethods returned error: %v", err)
	}
	if len(methods) != 0 {
		t.Fatalf("methods = %#v, want none without saveCard", methods)
	}
}

func TestFakeProviderMarkSessionPaidSavesCard(t *testing.T) {
	provider := NewFakeProvider()
	input := testPaymentSessionInput("ord0000000000000000000000a")
	session, err := provider.CreatePaymentSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}

	if _, err := provider.MarkSessionPaid(session.ID, true); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}
	// Replayed fake-pay POSTs stay idempotent: same card, recorded once.
	if _, err := provider.MarkSessionPaid(session.ID, true); err != nil {
		t.Fatalf("repeated MarkSessionPaid returned error: %v", err)
	}

	methods, err := provider.ListPaymentMethods(context.Background(), input.StripeCustomerID)
	if err != nil {
		t.Fatalf("ListPaymentMethods returned error: %v", err)
	}
	want := PaymentMethod{ID: "pm_fake_visa_4242", Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2034}
	if len(methods) != 1 || methods[0] != want {
		t.Fatalf("methods = %#v, want exactly %#v", methods, want)
	}
}

func TestFakeProviderExpireSession(t *testing.T) {
	provider := NewFakeProvider()
	input := testPaymentSessionInput("ord0000000000000000000000a")
	session, err := provider.CreatePaymentSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}

	if err := provider.ExpireSession(context.Background(), "cs_fake_missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ExpireSession on unknown session error = %v, want %v", err, ErrSessionNotFound)
	}

	if err := provider.ExpireSession(context.Background(), session.ID); err != nil {
		t.Fatalf("ExpireSession returned error: %v", err)
	}
	expired, err := provider.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if expired.Status != SessionStatusExpired {
		t.Fatalf("Status = %q, want %q", expired.Status, SessionStatusExpired)
	}
	if expired.URL != "" {
		t.Fatalf("URL = %q, want empty once expired (no longer payable)", expired.URL)
	}

	// Expired sessions can never collect money.
	if _, err := provider.MarkSessionPaid(session.ID, false); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("MarkSessionPaid on expired session error = %v, want %v", err, ErrSessionExpired)
	}
	if got, err := provider.GetSession(context.Background(), session.ID); err != nil || got.PaymentStatus != "unpaid" {
		t.Fatalf("session after rejected payment = %#v, err %v, want it left unpaid", got, err)
	}

	// Re-expiring is an idempotent success.
	if err := provider.ExpireSession(context.Background(), session.ID); err != nil {
		t.Fatalf("repeated ExpireSession returned error: %v", err)
	}
}

func TestFakeProviderExpireSessionRefusesCompletedSessions(t *testing.T) {
	provider := NewFakeProvider()
	session, err := provider.CreatePaymentSession(context.Background(), testPaymentSessionInput("ord0000000000000000000000a"))
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}
	if _, err := provider.MarkSessionPaid(session.ID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	if err := provider.ExpireSession(context.Background(), session.ID); err == nil {
		t.Fatal("ExpireSession on a completed session succeeded, want a refusal (the Stripe contract)")
	}
	paid, err := provider.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if paid.Status != SessionStatusComplete || paid.PaymentStatus != "paid" {
		t.Fatalf("session = %#v, want it left complete/paid", paid)
	}
}

func TestFakeProviderMarkSessionPaidRejectsUnknownAndSetupSessions(t *testing.T) {
	provider := NewFakeProvider()
	if _, err := provider.MarkSessionPaid("cs_fake_missing", false); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("MarkSessionPaid error = %v, want %v", err, ErrSessionNotFound)
	}

	setup, err := provider.CreateSetupSession(context.Background(), SetupSessionInput{
		CustomerID:       "cus0000000000000000000000a",
		StripeCustomerID: "cus_fake_cus0000000000000000000000a",
		SuccessURL:       "http://127.0.0.1:8080/account/payment-methods?saved=1",
		CancelURL:        "http://127.0.0.1:8080/account/payment-methods",
	})
	if err != nil {
		t.Fatalf("CreateSetupSession returned error: %v", err)
	}
	if _, err := provider.MarkSessionPaid(setup.ID, false); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("MarkSessionPaid on setup session error = %v, want %v", err, ErrSessionNotFound)
	}
}

func TestFakeProviderSetupSessionLifecycle(t *testing.T) {
	provider := NewFakeProvider()
	input := SetupSessionInput{
		CustomerID:       "cus0000000000000000000000a",
		StripeCustomerID: "cus_fake_cus0000000000000000000000a",
		SuccessURL:       "http://127.0.0.1:8080/account/payment-methods?saved=1",
		CancelURL:        "http://127.0.0.1:8080/account/payment-methods",
	}

	session, err := provider.CreateSetupSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreateSetupSession returned error: %v", err)
	}
	if session.ID != "cs_fake_setup_cus0000000000000000000000a" {
		t.Fatalf("session.ID = %q, want deterministic setup id", session.ID)
	}
	if session.URL != "/checkout/fake-pay?session_id=cs_fake_setup_cus0000000000000000000000a" {
		t.Fatalf("session.URL = %q, want sentinel fake-pay URL", session.URL)
	}
	if session.Mode != "setup" || session.PaymentStatus != "no_payment_required" {
		t.Fatalf("session = %#v, want setup/no_payment_required", session)
	}

	successURL, err := provider.MarkSetupComplete(session.ID)
	if err != nil {
		t.Fatalf("MarkSetupComplete returned error: %v", err)
	}
	if successURL != input.SuccessURL {
		t.Fatalf("successURL = %q, want %q", successURL, input.SuccessURL)
	}

	methods, err := provider.ListPaymentMethods(context.Background(), input.StripeCustomerID)
	if err != nil {
		t.Fatalf("ListPaymentMethods returned error: %v", err)
	}
	if len(methods) != 1 || methods[0].ID != "pm_fake_visa_4242" {
		t.Fatalf("methods = %#v, want pm_fake_visa_4242", methods)
	}
}

func TestFakeProviderMarkSetupCompleteRejectsUnknownAndPaymentSessions(t *testing.T) {
	provider := NewFakeProvider()
	if _, err := provider.MarkSetupComplete("cs_fake_setup_missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("MarkSetupComplete error = %v, want %v", err, ErrSessionNotFound)
	}

	payment, err := provider.CreatePaymentSession(context.Background(), testPaymentSessionInput("ord0000000000000000000000a"))
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}
	if _, err := provider.MarkSetupComplete(payment.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("MarkSetupComplete on payment session error = %v, want %v", err, ErrSessionNotFound)
	}
}

func TestFakeProviderSessionRedirectURLs(t *testing.T) {
	provider := NewFakeProvider()
	input := testPaymentSessionInput("ord0000000000000000000000a")
	session, err := provider.CreatePaymentSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}

	successURL, cancelURL, ok := provider.SessionRedirectURLs(session.ID)
	if !ok {
		t.Fatal("SessionRedirectURLs ok = false, want true")
	}
	if successURL != "http://127.0.0.1:8080/checkout/confirm?session_id=cs_fake_ord0000000000000000000000a" {
		t.Fatalf("successURL = %q, want substituted session id", successURL)
	}
	if cancelURL != input.CancelURL {
		t.Fatalf("cancelURL = %q, want %q", cancelURL, input.CancelURL)
	}

	if _, _, ok := provider.SessionRedirectURLs("cs_fake_missing"); ok {
		t.Fatal("SessionRedirectURLs ok = true for unknown session, want false")
	}
}

func TestFakeProviderDetachPaymentMethod(t *testing.T) {
	provider := NewFakeProvider()
	input := testPaymentSessionInput("ord0000000000000000000000a")
	session, err := provider.CreatePaymentSession(context.Background(), input)
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}
	if _, err := provider.MarkSessionPaid(session.ID, true); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	if err := provider.DetachPaymentMethod(context.Background(), "cus_fake_other", "pm_fake_visa_4242"); !errors.Is(err, ErrPaymentMethodNotFound) {
		t.Fatalf("DetachPaymentMethod for foreign customer error = %v, want %v", err, ErrPaymentMethodNotFound)
	}

	if err := provider.DetachPaymentMethod(context.Background(), input.StripeCustomerID, "pm_fake_visa_4242"); err != nil {
		t.Fatalf("DetachPaymentMethod returned error: %v", err)
	}
	methods, err := provider.ListPaymentMethods(context.Background(), input.StripeCustomerID)
	if err != nil {
		t.Fatalf("ListPaymentMethods returned error: %v", err)
	}
	if len(methods) != 0 {
		t.Fatalf("methods = %#v, want empty after detach", methods)
	}

	if err := provider.DetachPaymentMethod(context.Background(), input.StripeCustomerID, "pm_fake_visa_4242"); !errors.Is(err, ErrPaymentMethodNotFound) {
		t.Fatalf("repeated DetachPaymentMethod error = %v, want %v", err, ErrPaymentMethodNotFound)
	}
}

func TestFakeProviderListPaymentMethodsReturnsCopy(t *testing.T) {
	provider := NewFakeProvider()
	session, err := provider.CreatePaymentSession(context.Background(), testPaymentSessionInput("ord0000000000000000000000a"))
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}
	if _, err := provider.MarkSessionPaid(session.ID, true); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	methods, err := provider.ListPaymentMethods(context.Background(), "cus_fake_cus0000000000000000000000a")
	if err != nil {
		t.Fatalf("ListPaymentMethods returned error: %v", err)
	}
	methods[0].ID = "pm_mutated"

	again, err := provider.ListPaymentMethods(context.Background(), "cus_fake_cus0000000000000000000000a")
	if err != nil {
		t.Fatalf("ListPaymentMethods returned error: %v", err)
	}
	if again[0].ID != "pm_fake_visa_4242" {
		t.Fatalf("stored method = %#v, want unaffected by caller mutation", again[0])
	}
}

func TestFakeProviderParseWebhook(t *testing.T) {
	provider := NewFakeProvider()
	payload := checkoutSessionCompletedPayload(testCheckoutSessionJSON)
	signedAt := testWebhookNow.Add(-time.Minute)

	tests := []struct {
		name    string
		payload []byte
		header  string
		now     time.Time
		wantErr error
	}{
		{
			name:    "valid signature against the fixed demo secret",
			payload: payload,
			header:  stripeSignatureHeader(payload, FakeWebhookSigningSecret, signedAt),
			now:     testWebhookNow,
		},
		{
			name:    "tampered payload",
			payload: checkoutSessionCompletedPayload(`{"id":"cs_test_123","object":"checkout.session","amount_total":1}`),
			header:  stripeSignatureHeader(payload, FakeWebhookSigningSecret, signedAt),
			now:     testWebhookNow,
			wantErr: webhook.ErrNoValidSignature,
		},
		{
			name:    "stale timestamp rejected against injected clock",
			payload: payload,
			header:  stripeSignatureHeader(payload, FakeWebhookSigningSecret, signedAt),
			now:     signedAt.Add(webhookSignatureTolerance + time.Second),
			wantErr: webhook.ErrTooOld,
		},
		{
			name:    "signature from the real-looking but wrong secret",
			payload: payload,
			header:  stripeSignatureHeader(payload, testWebhookSigningSecret, signedAt),
			now:     testWebhookNow,
			wantErr: webhook.ErrNoValidSignature,
		},
		{
			name:    "missing header",
			payload: payload,
			header:  "",
			now:     testWebhookNow,
			wantErr: webhook.ErrNotSigned,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, err := provider.ParseWebhook(test.payload, test.header, test.now)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("ParseWebhook error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWebhook returned error: %v", err)
			}
			if event.Type != "checkout.session.completed" || event.OrderID != "ord0000000000000000000000a" {
				t.Fatalf("event = %#v, want mapped checkout.session.completed", event)
			}
		})
	}
}
