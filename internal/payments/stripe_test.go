package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

const testWebhookSigningSecret = "whsec_test_placeholder"

var testWebhookNow = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

// stripeSignatureHeader hand-builds a Stripe-Signature header:
// t=<unix>,v1=<hex hmac-sha256(secret, "<unix>.<payload>")>.
func stripeSignatureHeader(payload []byte, secret string, timestamp time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", timestamp.Unix(), payload)

	return fmt.Sprintf("t=%d,v1=%s", timestamp.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func checkoutSessionCompletedPayload(sessionJSON string) []byte {
	return fmt.Appendf(nil, `{"id":"evt_test_1","object":"event","type":"checkout.session.completed","data":{"object":%s}}`, sessionJSON)
}

const testCheckoutSessionJSON = `{
	"id": "cs_test_123",
	"object": "checkout.session",
	"client_reference_id": "ref0000000000000000000000a",
	"metadata": {"order_id": "ord0000000000000000000000a", "customer_id": "cus0000000000000000000000a"},
	"mode": "payment",
	"status": "complete",
	"payment_status": "paid",
	"amount_total": 4200,
	"payment_intent": "pi_test_123"
}`

func TestStripeCredentialsFromSecretString(t *testing.T) {
	tests := []struct {
		name       string
		secret     string
		want       StripeCredentials
		wantErr    error
		wantAnyErr bool
	}{
		{
			name:   "valid credentials",
			secret: `{"secret_key":"sk_test_placeholder","webhook_signing_secret":"whsec_test_placeholder"}`,
			want:   StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: "whsec_test_placeholder"},
		},
		{
			name:   "values are trimmed",
			secret: `{"secret_key":"  sk_test_placeholder  ","webhook_signing_secret":"  whsec_test_placeholder  "}`,
			want:   StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: "whsec_test_placeholder"},
		},
		{
			name:    "missing secret key",
			secret:  `{"webhook_signing_secret":"whsec_test_placeholder"}`,
			wantErr: ErrStripeCredentialsNotConfigured,
		},
		{
			name:    "missing webhook signing secret",
			secret:  `{"secret_key":"sk_test_placeholder"}`,
			wantErr: ErrStripeCredentialsNotConfigured,
		},
		{
			name:    "empty object",
			secret:  `{}`,
			wantErr: ErrStripeCredentialsNotConfigured,
		},
		{
			name:       "malformed JSON",
			secret:     `{"secret_key":`,
			wantAnyErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials, err := stripeCredentialsFromSecretString(test.secret)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("stripeCredentialsFromSecretString error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if test.wantAnyErr {
				if err == nil {
					t.Fatal("stripeCredentialsFromSecretString returned nil error, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("stripeCredentialsFromSecretString returned error: %v", err)
			}
			if credentials != test.want {
				t.Fatalf("credentials = %#v, want %#v", credentials, test.want)
			}
		})
	}
}

func TestStripeCredentialsFromEnvironmentRequiresValue(t *testing.T) {
	t.Setenv(EnvStripeCredentialsSecretJSON, "   ")
	if _, err := StripeCredentialsFromEnvironment(context.Background()); !errors.Is(err, ErrStripeCredentialsNotConfigured) {
		t.Fatalf("StripeCredentialsFromEnvironment error = %v, want %v", err, ErrStripeCredentialsNotConfigured)
	}

	t.Setenv(EnvStripeCredentialsSecretJSON, testValidStripeCredentialsJSON)
	credentials, err := StripeCredentialsFromEnvironment(context.Background())
	if err != nil {
		t.Fatalf("StripeCredentialsFromEnvironment returned error: %v", err)
	}
	if credentials.SecretKey != "sk_test_placeholder" || credentials.WebhookSigningSecret != "whsec_test_placeholder" {
		t.Fatalf("credentials = %#v, want parsed placeholders", credentials)
	}
}

func TestCheckoutSessionCreateParams(t *testing.T) {
	expiresAt := time.Date(2026, 6, 9, 12, 30, 0, 0, time.UTC)
	input := PaymentSessionInput{
		OrderID:          "ord0000000000000000000000a",
		CustomerID:       "cus0000000000000000000000a",
		StripeCustomerID: "cus_stripe123",
		Email:            "shopper@example.test",
		Lines: []SessionLine{
			{Name: "Mango Sticky Rice Candy", UnitAmountCents: 450, Quantity: 2},
			{Name: "Indigo Scarf — Natural Dye", UnitAmountCents: 2900, Quantity: 1},
		},
		TotalCents: 3800,
		SuccessURL: "https://thailandgiftshop.com/checkout/confirm?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:  "https://thailandgiftshop.com/checkout?canceled=1",
		ExpiresAt:  expiresAt,
		Attempt:    2,
	}

	params := checkoutSessionCreateParams(input)

	if got := stringValue(params.Mode); got != "payment" {
		t.Fatalf("Mode = %q, want %q", got, "payment")
	}
	if got := stringValue(params.ClientReferenceID); got != input.OrderID {
		t.Fatalf("ClientReferenceID = %q, want %q", got, input.OrderID)
	}
	if got := stringValue(params.Customer); got != input.StripeCustomerID {
		t.Fatalf("Customer = %q, want %q", got, input.StripeCustomerID)
	}
	if params.Metadata["order_id"] != input.OrderID || params.Metadata["customer_id"] != input.CustomerID {
		t.Fatalf("Metadata = %#v, want order_id and customer_id", params.Metadata)
	}
	if params.SavedPaymentMethodOptions == nil || stringValue(params.SavedPaymentMethodOptions.PaymentMethodSave) != "enabled" {
		t.Fatalf("SavedPaymentMethodOptions = %#v, want payment_method_save=enabled", params.SavedPaymentMethodOptions)
	}
	if params.CustomerEmail != nil {
		t.Fatalf("CustomerEmail = %q, want nil for customer sessions (the Stripe Customer carries the email)", stringValue(params.CustomerEmail))
	}
	if got := int64Value(params.ExpiresAt); got != expiresAt.Unix() {
		t.Fatalf("ExpiresAt = %d, want %d", got, expiresAt.Unix())
	}
	if got := stringValue(params.SuccessURL); got != input.SuccessURL {
		t.Fatalf("SuccessURL = %q, want %q", got, input.SuccessURL)
	}
	if got := stringValue(params.CancelURL); got != input.CancelURL {
		t.Fatalf("CancelURL = %q, want %q", got, input.CancelURL)
	}
	if len(params.LineItems) != len(input.Lines) {
		t.Fatalf("len(LineItems) = %d, want %d", len(params.LineItems), len(input.Lines))
	}
	for index, line := range input.Lines {
		item := params.LineItems[index]
		if got := int64Value(item.Quantity); got != int64(line.Quantity) {
			t.Fatalf("LineItems[%d].Quantity = %d, want %d", index, got, line.Quantity)
		}
		if item.Price != nil {
			t.Fatalf("LineItems[%d].Price = %q, want only price_data from the snapshot", index, stringValue(item.Price))
		}
		if item.PriceData == nil {
			t.Fatalf("LineItems[%d].PriceData = nil, want snapshot price_data", index)
		}
		if got := stringValue(item.PriceData.Currency); got != "usd" {
			t.Fatalf("LineItems[%d].PriceData.Currency = %q, want %q", index, got, "usd")
		}
		if got := int64Value(item.PriceData.UnitAmount); got != int64(line.UnitAmountCents) {
			t.Fatalf("LineItems[%d].PriceData.UnitAmount = %d, want %d", index, got, line.UnitAmountCents)
		}
		if item.PriceData.ProductData == nil || stringValue(item.PriceData.ProductData.Name) != line.Name {
			t.Fatalf("LineItems[%d].PriceData.ProductData = %#v, want name %q", index, item.PriceData.ProductData, line.Name)
		}
	}
	if got := stringValue(params.IdempotencyKey); got != "order-session-ord0000000000000000000000a-2" {
		t.Fatalf("IdempotencyKey = %q, want %q", got, "order-session-ord0000000000000000000000a-2")
	}
}

// TestCheckoutSessionCreateParamsGuest pins the guest branch: no Stripe
// Customer, no saved-payment-method options (both require a Customer), the
// hosted page's email locked to the checkout form's value, and no empty
// customer_id metadata entry.
func TestCheckoutSessionCreateParamsGuest(t *testing.T) {
	expiresAt := time.Date(2026, 6, 9, 12, 30, 0, 0, time.UTC)
	params := checkoutSessionCreateParams(PaymentSessionInput{
		OrderID:          "ord0000000000000000000000b",
		CustomerID:       "",
		StripeCustomerID: "",
		Email:            "guest@example.test",
		Lines:            []SessionLine{{Name: "Mango Sticky Rice Candy", UnitAmountCents: 450, Quantity: 2}},
		TotalCents:       900,
		SuccessURL:       "https://thailandgiftshop.com/checkout/confirm?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:        "https://thailandgiftshop.com/checkout?canceled=1",
		ExpiresAt:        expiresAt,
		Attempt:          1,
	})

	if params.Customer != nil {
		t.Fatalf("Customer = %q, want nil for guest sessions", stringValue(params.Customer))
	}
	if params.SavedPaymentMethodOptions != nil {
		t.Fatalf("SavedPaymentMethodOptions = %#v, want nil for guest sessions", params.SavedPaymentMethodOptions)
	}
	if got := stringValue(params.CustomerEmail); got != "guest@example.test" {
		t.Fatalf("CustomerEmail = %q, want the checkout form email", got)
	}
	if params.Metadata["order_id"] != "ord0000000000000000000000b" {
		t.Fatalf("Metadata = %#v, want order_id", params.Metadata)
	}
	if _, found := params.Metadata["customer_id"]; found {
		t.Fatalf("Metadata = %#v, want no customer_id key for guest sessions", params.Metadata)
	}
}

func TestStripeProviderKind(t *testing.T) {
	provider := NewStripeProvider(StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: testWebhookSigningSecret})
	if provider.Kind() != KindStripe {
		t.Fatalf("Kind() = %q, want %q", provider.Kind(), KindStripe)
	}
}

func TestStripeProviderParseWebhook(t *testing.T) {
	provider := NewStripeProvider(StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: testWebhookSigningSecret})
	payload := checkoutSessionCompletedPayload(testCheckoutSessionJSON)
	signedAt := testWebhookNow.Add(-time.Minute)

	tests := []struct {
		name      string
		payload   []byte
		header    string
		now       time.Time
		wantErr   error
		wantEvent bool
	}{
		{
			name:      "valid signature within tolerance",
			payload:   payload,
			header:    stripeSignatureHeader(payload, testWebhookSigningSecret, signedAt),
			now:       testWebhookNow,
			wantEvent: true,
		},
		{
			name:      "timestamp exactly at tolerance boundary",
			payload:   payload,
			header:    stripeSignatureHeader(payload, testWebhookSigningSecret, signedAt),
			now:       signedAt.Add(webhookSignatureTolerance),
			wantEvent: true,
		},
		{
			name:    "tampered payload",
			payload: checkoutSessionCompletedPayload(`{"id":"cs_test_123","object":"checkout.session","amount_total":1}`),
			header:  stripeSignatureHeader(payload, testWebhookSigningSecret, signedAt),
			now:     testWebhookNow,
			wantErr: webhook.ErrNoValidSignature,
		},
		{
			name:    "stale timestamp rejected against injected clock",
			payload: payload,
			header:  stripeSignatureHeader(payload, testWebhookSigningSecret, signedAt),
			now:     signedAt.Add(webhookSignatureTolerance + time.Second),
			wantErr: webhook.ErrTooOld,
		},
		{
			name:    "missing header",
			payload: payload,
			header:  "",
			now:     testWebhookNow,
			wantErr: webhook.ErrNotSigned,
		},
		{
			name:    "header without timestamp",
			payload: payload,
			header:  "v1=deadbeef",
			now:     testWebhookNow,
			wantErr: webhook.ErrInvalidHeader,
		},
		{
			name:    "header with malformed timestamp",
			payload: payload,
			header:  "t=notanumber,v1=deadbeef",
			now:     testWebhookNow,
			wantErr: webhook.ErrInvalidHeader,
		},
		{
			name:    "signature from a different secret",
			payload: payload,
			header:  stripeSignatureHeader(payload, "whsec_other_placeholder", signedAt),
			now:     testWebhookNow,
			wantErr: webhook.ErrNoValidSignature,
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
			if !test.wantEvent {
				return
			}
			if event.ID != "evt_test_1" || event.Type != "checkout.session.completed" {
				t.Fatalf("event = %#v, want id evt_test_1 type checkout.session.completed", event)
			}
			if event.SessionID != "cs_test_123" || event.Session.ID != "cs_test_123" {
				t.Fatalf("event session id = %q/%q, want cs_test_123", event.SessionID, event.Session.ID)
			}
			if event.OrderID != "ord0000000000000000000000a" || event.Session.OrderID != "ord0000000000000000000000a" {
				t.Fatalf("event order id = %q/%q, want metadata order_id", event.OrderID, event.Session.OrderID)
			}
			if event.Session.PaymentStatus != "paid" {
				t.Fatalf("PaymentStatus = %q, want %q", event.Session.PaymentStatus, "paid")
			}
			if event.Session.Status != SessionStatusComplete {
				t.Fatalf("Status = %q, want %q", event.Session.Status, SessionStatusComplete)
			}
			if event.Session.PaymentIntentID != "pi_test_123" {
				t.Fatalf("PaymentIntentID = %q, want %q", event.Session.PaymentIntentID, "pi_test_123")
			}
			if event.Session.AmountTotalCents != 4200 {
				t.Fatalf("AmountTotalCents = %d, want 4200", event.Session.AmountTotalCents)
			}
			if event.Session.Mode != "payment" {
				t.Fatalf("Mode = %q, want %q", event.Session.Mode, "payment")
			}
			if event.Session.CardBrand != "" || event.Session.CardLast4 != "" {
				t.Fatalf("card = %q/%q, want empty without an expanded payment intent", event.Session.CardBrand, event.Session.CardLast4)
			}
		})
	}
}

func TestParseWebhookOrderIDFallsBackToClientReferenceID(t *testing.T) {
	provider := NewStripeProvider(StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: testWebhookSigningSecret})
	payload := checkoutSessionCompletedPayload(`{
		"id": "cs_test_456",
		"object": "checkout.session",
		"client_reference_id": "ref0000000000000000000000b",
		"mode": "payment",
		"payment_status": "unpaid",
		"amount_total": 1000
	}`)
	header := stripeSignatureHeader(payload, testWebhookSigningSecret, testWebhookNow)

	event, err := provider.ParseWebhook(payload, header, testWebhookNow)
	if err != nil {
		t.Fatalf("ParseWebhook returned error: %v", err)
	}
	if event.OrderID != "ref0000000000000000000000b" {
		t.Fatalf("OrderID = %q, want client_reference_id fallback", event.OrderID)
	}
}

func TestParseWebhookExpandedPaymentIntentFillsCardDetails(t *testing.T) {
	provider := NewStripeProvider(StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: testWebhookSigningSecret})
	payload := checkoutSessionCompletedPayload(`{
		"id": "cs_test_789",
		"object": "checkout.session",
		"metadata": {"order_id": "ord0000000000000000000000c"},
		"mode": "payment",
		"payment_status": "paid",
		"amount_total": 2500,
		"payment_intent": {
			"id": "pi_test_789",
			"object": "payment_intent",
			"payment_method": {
				"id": "pm_test_789",
				"object": "payment_method",
				"card": {"brand": "visa", "last4": "4242"}
			}
		}
	}`)
	header := stripeSignatureHeader(payload, testWebhookSigningSecret, testWebhookNow)

	event, err := provider.ParseWebhook(payload, header, testWebhookNow)
	if err != nil {
		t.Fatalf("ParseWebhook returned error: %v", err)
	}
	if event.Session.PaymentIntentID != "pi_test_789" {
		t.Fatalf("PaymentIntentID = %q, want %q", event.Session.PaymentIntentID, "pi_test_789")
	}
	if event.Session.CardBrand != "visa" || event.Session.CardLast4 != "4242" {
		t.Fatalf("card = %q/%q, want visa/4242", event.Session.CardBrand, event.Session.CardLast4)
	}
}

func TestParseWebhookSetupModeSession(t *testing.T) {
	provider := NewStripeProvider(StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: testWebhookSigningSecret})
	payload := fmt.Appendf(nil, `{"id":"evt_test_2","object":"event","type":"checkout.session.completed","data":{"object":%s}}`, `{
		"id": "cs_test_setup",
		"object": "checkout.session",
		"mode": "setup",
		"payment_status": "no_payment_required"
	}`)
	header := stripeSignatureHeader(payload, testWebhookSigningSecret, testWebhookNow)

	event, err := provider.ParseWebhook(payload, header, testWebhookNow)
	if err != nil {
		t.Fatalf("ParseWebhook returned error: %v", err)
	}
	if event.Session.Mode != "setup" || event.Session.PaymentStatus != "no_payment_required" {
		t.Fatalf("session = %#v, want setup/no_payment_required", event.Session)
	}
}

func TestParseWebhookIgnoresNonCheckoutEventBodies(t *testing.T) {
	provider := NewStripeProvider(StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: testWebhookSigningSecret})
	payload := []byte(`{"id":"evt_test_3","object":"event","type":"payment_intent.succeeded","data":{"object":{"id":"pi_test_3","object":"payment_intent"}}}`)
	header := stripeSignatureHeader(payload, testWebhookSigningSecret, testWebhookNow)

	event, err := provider.ParseWebhook(payload, header, testWebhookNow)
	if err != nil {
		t.Fatalf("ParseWebhook returned error: %v", err)
	}
	if event.ID != "evt_test_3" || event.Type != "payment_intent.succeeded" {
		t.Fatalf("event = %#v, want id/type passthrough", event)
	}
	if event.SessionID != "" || event.OrderID != "" || event.Session != (Session{}) {
		t.Fatalf("event = %#v, want empty session fields for non-checkout events", event)
	}
}

func TestRefundIdempotencyKey(t *testing.T) {
	if got, want := refundIdempotencyKey("ord0000000000000000000000a", 2), "order-refund-ord0000000000000000000000a-2"; got != want {
		t.Fatalf("refundIdempotencyKey = %q, want %q", got, want)
	}
}

func TestRefundCreateParams(t *testing.T) {
	params := refundCreateParams(RefundInput{
		OrderID:         "ord0000000000000000000000a",
		PaymentIntentID: "pi_test_123",
		Attempt:         3,
	})

	if got := stringValue(params.PaymentIntent); got != "pi_test_123" {
		t.Fatalf("PaymentIntent = %q, want %q", got, "pi_test_123")
	}
	if params.Amount != nil {
		t.Fatalf("Amount = %d, want nil (full refund)", *params.Amount)
	}
	if got := stringValue(params.Reason); got != "requested_by_customer" {
		t.Fatalf("Reason = %q, want %q", got, "requested_by_customer")
	}
	if params.Metadata["order_id"] != "ord0000000000000000000000a" || params.Metadata["attempt"] != "3" {
		t.Fatalf("Metadata = %#v, want order_id and attempt", params.Metadata)
	}
	if got := stringValue(params.IdempotencyKey); got != "order-refund-ord0000000000000000000000a-3" {
		t.Fatalf("IdempotencyKey = %q, want %q", got, "order-refund-ord0000000000000000000000a-3")
	}
}

func TestRefundFromStripeRefund(t *testing.T) {
	tests := []struct {
		name       string
		refundJSON string
		want       Refund
	}{
		{
			name: "full metadata with bare payment intent",
			refundJSON: `{
				"id": "re_test_1",
				"object": "refund",
				"status": "succeeded",
				"amount": 4200,
				"payment_intent": "pi_test_123",
				"metadata": {"order_id": "ord0000000000000000000000a", "attempt": "2"}
			}`,
			want: Refund{ID: "re_test_1", PaymentIntentID: "pi_test_123", OrderID: "ord0000000000000000000000a", Attempt: 2, Status: "succeeded", AmountCents: 4200},
		},
		{
			name: "failed refund carries the failure reason",
			refundJSON: `{
				"id": "re_test_2",
				"object": "refund",
				"status": "failed",
				"failure_reason": "expired_or_canceled_card",
				"amount": 4200,
				"payment_intent": "pi_test_123",
				"metadata": {"order_id": "ord0000000000000000000000a", "attempt": "1"}
			}`,
			want: Refund{ID: "re_test_2", PaymentIntentID: "pi_test_123", OrderID: "ord0000000000000000000000a", Attempt: 1, Status: "failed", AmountCents: 4200, FailureReason: "expired_or_canceled_card"},
		},
		{
			name: "absent metadata parses to empty order id and attempt 0",
			refundJSON: `{
				"id": "re_test_3",
				"object": "refund",
				"status": "pending",
				"amount": 100
			}`,
			want: Refund{ID: "re_test_3", Status: "pending", AmountCents: 100},
		},
		{
			name: "garbage attempt metadata parses to 0",
			refundJSON: `{
				"id": "re_test_4",
				"object": "refund",
				"status": "pending",
				"amount": 100,
				"metadata": {"order_id": "ord0000000000000000000000a", "attempt": "soon"}
			}`,
			want: Refund{ID: "re_test_4", OrderID: "ord0000000000000000000000a", Status: "pending", AmountCents: 100},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stripeRefund stripe.Refund
			if err := json.Unmarshal([]byte(test.refundJSON), &stripeRefund); err != nil {
				t.Fatalf("unmarshal refund fixture: %v", err)
			}
			if got := refundFromStripeRefund(&stripeRefund); got != test.want {
				t.Fatalf("refundFromStripeRefund = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseWebhookRefundEvents(t *testing.T) {
	provider := NewStripeProvider(StripeCredentials{SecretKey: "sk_test_placeholder", WebhookSigningSecret: testWebhookSigningSecret})

	t.Run("refund.created with metadata and bare payment intent", func(t *testing.T) {
		payload := []byte(`{"id":"evt_refund_1","object":"event","type":"refund.created","data":{"object":{
			"id": "re_test_1",
			"object": "refund",
			"status": "pending",
			"amount": 4200,
			"payment_intent": "pi_test_123",
			"metadata": {"order_id": "ord0000000000000000000000a", "attempt": "2"}
		}}}`)
		header := stripeSignatureHeader(payload, testWebhookSigningSecret, testWebhookNow)

		event, err := provider.ParseWebhook(payload, header, testWebhookNow)
		if err != nil {
			t.Fatalf("ParseWebhook returned error: %v", err)
		}
		if event.ID != "evt_refund_1" || event.Type != "refund.created" {
			t.Fatalf("event = %#v, want refund.created envelope", event)
		}
		if event.OrderID != "ord0000000000000000000000a" {
			t.Fatalf("event.OrderID = %q, want order id from metadata", event.OrderID)
		}
		want := Refund{ID: "re_test_1", PaymentIntentID: "pi_test_123", OrderID: "ord0000000000000000000000a", Attempt: 2, Status: "pending", AmountCents: 4200}
		if event.Refund != want {
			t.Fatalf("event.Refund = %#v, want %#v", event.Refund, want)
		}
		if event.SessionID != "" || event.Session != (Session{}) {
			t.Fatalf("event = %#v, want empty session fields for refund events", event)
		}
	})

	t.Run("refund.failed carries failure reason", func(t *testing.T) {
		payload := []byte(`{"id":"evt_refund_2","object":"event","type":"refund.failed","data":{"object":{
			"id": "re_test_2",
			"object": "refund",
			"status": "failed",
			"failure_reason": "expired_or_canceled_card",
			"amount": 4200,
			"payment_intent": "pi_test_123",
			"metadata": {"order_id": "ord0000000000000000000000a", "attempt": "1"}
		}}}`)
		header := stripeSignatureHeader(payload, testWebhookSigningSecret, testWebhookNow)

		event, err := provider.ParseWebhook(payload, header, testWebhookNow)
		if err != nil {
			t.Fatalf("ParseWebhook returned error: %v", err)
		}
		if event.Refund.Status != RefundStatusFailed || event.Refund.FailureReason != "expired_or_canceled_card" || event.Refund.Attempt != 1 {
			t.Fatalf("event.Refund = %#v, want failed with reason and attempt", event.Refund)
		}
	})

	t.Run("dashboard refund without metadata has empty order id", func(t *testing.T) {
		payload := []byte(`{"id":"evt_refund_3","object":"event","type":"refund.updated","data":{"object":{
			"id": "re_test_3",
			"object": "refund",
			"status": "succeeded",
			"amount": 100,
			"payment_intent": "pi_test_999"
		}}}`)
		header := stripeSignatureHeader(payload, testWebhookSigningSecret, testWebhookNow)

		event, err := provider.ParseWebhook(payload, header, testWebhookNow)
		if err != nil {
			t.Fatalf("ParseWebhook returned error: %v", err)
		}
		if event.OrderID != "" || event.Refund.OrderID != "" || event.Refund.Attempt != 0 {
			t.Fatalf("event = %#v, want empty order id and attempt for dashboard refunds", event)
		}
		if event.Refund.ID != "re_test_3" || event.Refund.PaymentIntentID != "pi_test_999" {
			t.Fatalf("event.Refund = %#v, want id and payment intent mapped", event.Refund)
		}
	})

	t.Run("refund event without payload errors", func(t *testing.T) {
		payload := []byte(`{"id":"evt_refund_4","object":"event","type":"refund.created"}`)
		header := stripeSignatureHeader(payload, testWebhookSigningSecret, testWebhookNow)

		if _, err := provider.ParseWebhook(payload, header, testWebhookNow); err == nil {
			t.Fatal("ParseWebhook returned nil error, want missing-payload error")
		}
	})
}

func TestIsStripeChargeAlreadyRefunded(t *testing.T) {
	already := &stripe.Error{Code: stripe.ErrorCodeChargeAlreadyRefunded}
	if !isStripeChargeAlreadyRefunded(fmt.Errorf("create stripe refund: %w", already)) {
		t.Fatal("isStripeChargeAlreadyRefunded(charge_already_refunded) = false, want true")
	}
	if isStripeChargeAlreadyRefunded(&stripe.Error{Code: stripe.ErrorCodeCardDeclined}) {
		t.Fatal("isStripeChargeAlreadyRefunded(card_declined) = true, want false")
	}
	if isStripeChargeAlreadyRefunded(errors.New("network down")) {
		t.Fatal("isStripeChargeAlreadyRefunded(plain error) = true, want false")
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}

	return *value
}
