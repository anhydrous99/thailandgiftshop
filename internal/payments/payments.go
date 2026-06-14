// Package payments abstracts the hosted-checkout payment provider behind a
// narrow interface so the storefront can run against Stripe in production and
// a deterministic in-memory fake everywhere else. Card data never passes
// through this package: only opaque provider IDs plus brand/last4 display
// strings cross the boundary.
package payments

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/awsconfig"
)

const (
	EnvStripeCredentialsSecretJSON = "STRIPE_CREDENTIALS_SECRET_JSON"
	EnvStripeCredentialsSecretName = "STRIPE_CREDENTIALS_SECRET_NAME"
	EnvPaymentsProvider            = "PAYMENTS_PROVIDER"
	EnvPublicBaseURL               = "PUBLIC_BASE_URL"

	DefaultPublicBaseURL = "https://thailandgiftshop.com"

	KindStripe = "stripe"
	KindFake   = "fake"
)

var (
	ErrPaymentsProviderNotConfigured      = errors.New("payments provider not configured")
	ErrFakeProviderNotAllowedInProduction = errors.New("fake payments provider is not allowed in production")
	ErrSessionNotFound                    = errors.New("payment session not found")
	ErrSessionExpired                     = errors.New("payment session expired")
	ErrPaymentMethodNotFound              = errors.New("payment method not found")
	// ErrRefundNotFound reports a refund ID the provider does not know.
	ErrRefundNotFound = errors.New("payment refund not found")
	// ErrChargeAlreadyRefunded reports a refund create rejected because the
	// charge is already fully refunded at the provider but no settled refund is
	// recorded on the order — the post-idempotency-window orphan case.
	// Resolution is manual reconciliation in the provider dashboard.
	ErrChargeAlreadyRefunded = errors.New("payment charge already fully refunded")
)

// Session lifecycle statuses (the Stripe checkout-session status values).
// Only open sessions are payable; complete sessions finished checkout (their
// payment may still be settling asynchronously); expired sessions can never
// collect money.
const (
	SessionStatusOpen     = "open"
	SessionStatusComplete = "complete"
	SessionStatusExpired  = "expired"
)

// Refund lifecycle statuses (the Stripe refund status values). pending and
// requires_action refunds are still settling; succeeded, failed, and canceled
// are terminal.
const (
	RefundStatusPending        = "pending"
	RefundStatusRequiresAction = "requires_action"
	RefundStatusSucceeded      = "succeeded"
	RefundStatusFailed         = "failed"
	RefundStatusCanceled       = "canceled"
)

// Provider is the payment gateway used by checkout orchestration. Sessions
// are hosted checkout pages: CreatePaymentSession charges an order
// (mode=payment) and CreateSetupSession saves a card for later (mode=setup).
type Provider interface {
	Kind() string
	EnsureCustomer(ctx context.Context, customerID string, email string) (string, error)
	CreatePaymentSession(ctx context.Context, input PaymentSessionInput) (Session, error)
	CreateSetupSession(ctx context.Context, input SetupSessionInput) (Session, error)
	GetSession(ctx context.Context, sessionID string) (Session, error)
	// ExpireSession closes an open hosted checkout session so it can no
	// longer be paid. Unknown sessions map to ErrSessionNotFound; expiring a
	// session that is not open is a provider error the caller classifies by
	// re-reading the session.
	ExpireSession(ctx context.Context, sessionID string) error
	// CreateRefund issues a full refund against the order's payment intent.
	// Idempotent per (OrderID, Attempt) via the provider idempotency key.
	CreateRefund(ctx context.Context, input RefundInput) (Refund, error)
	// GetRefund retrieves a refund for reconciliation. Unknown IDs map to
	// ErrRefundNotFound.
	GetRefund(ctx context.Context, refundID string) (Refund, error)
	ListPaymentMethods(ctx context.Context, stripeCustomerID string) ([]PaymentMethod, error)
	DetachPaymentMethod(ctx context.Context, stripeCustomerID string, paymentMethodID string) error
	ParseWebhook(payload []byte, signatureHeader string, now time.Time) (Event, error)
}

// Session is the provider-neutral view of a hosted checkout session.
// PaymentStatus is "paid" or "unpaid" for payment sessions and
// "no_payment_required" for setup sessions; Status is one of the
// SessionStatus* lifecycle values; Mode is "payment" or "setup". URL is set
// only while the session is open (payable).
type Session struct {
	ID               string
	URL              string
	OrderID          string
	PaymentIntentID  string
	PaymentStatus    string
	Status           string
	AmountTotalCents int
	CardBrand        string
	CardLast4        string
	Mode             string
}

// PaymentMethod carries the display-only saved-card fields. Nothing here is
// cardholder data: brand, last4, and expiry are what Stripe shows shoppers.
type PaymentMethod struct {
	ID       string
	Brand    string
	Last4    string
	ExpMonth int
	ExpYear  int
}

// Event is a verified webhook event. Session is populated only for
// checkout.session.* events; card details ride along only when the provider
// includes the expanded payment intent in the payload (Stripe webhook
// payloads carry the payment intent as a bare ID, so brand/last4 stay empty
// there — GetSession fills them during reconciliation).
type Event struct {
	ID        string
	Type      string
	SessionID string
	OrderID   string
	Session   Session
	// Refund is populated only for refund.* events; OrderID rides in the
	// refund's metadata (set by CreateRefund), so dashboard-issued refunds
	// without it carry an empty OrderID and are ignored downstream.
	Refund Refund
}

// RefundInput describes a full refund of an order's captured payment.
// Attempt is the per-order refund counter persisted on the order record; it
// scopes the provider idempotency key so a double-clicked refund reuses the
// same refund while a deliberate retry after a failure mints a fresh one.
type RefundInput struct {
	OrderID         string
	PaymentIntentID string
	Attempt         int
}

// Refund is the provider-neutral view of a refund. Status is one of the
// RefundStatus* values; FailureReason is set only for failed refunds.
// OrderID and Attempt ride in the refund's metadata (set by CreateRefund);
// both are zero for refunds we did not create.
type Refund struct {
	ID              string
	PaymentIntentID string
	OrderID         string
	Attempt         int
	Status          string
	AmountCents     int
	FailureReason   string
}

// SessionLine is one display line for the hosted checkout page, copied from
// the server-side order snapshot. Name already includes the variant label.
type SessionLine struct {
	Name            string
	UnitAmountCents int
	Quantity        int
}

// PaymentSessionInput describes the order being charged. Attempt is the
// per-order session counter persisted on the order record; it scopes the
// provider idempotency key so a retried place-order POST reuses the same
// session while a deliberate new attempt mints a fresh one.
type PaymentSessionInput struct {
	OrderID          string
	CustomerID       string
	StripeCustomerID string
	Email            string
	Lines            []SessionLine
	TotalCents       int
	SuccessURL       string
	CancelURL        string
	ExpiresAt        time.Time
	Attempt          int
}

type SetupSessionInput struct {
	CustomerID       string
	StripeCustomerID string
	SuccessURL       string
	CancelURL        string
}

// PublicBaseURLFromEnvironment returns the absolute origin used to build
// checkout success/cancel URLs, defaulting to the production apex.
func PublicBaseURLFromEnvironment() string {
	baseURL := strings.TrimSpace(os.Getenv(EnvPublicBaseURL))
	if baseURL == "" {
		return DefaultPublicBaseURL
	}

	return strings.TrimSuffix(baseURL, "/")
}

// NewProviderFromEnvironment selects the payment provider. An explicit
// PAYMENTS_PROVIDER wins, except that the fake provider is a hard error in
// production. Without an explicit choice, Stripe credentials select Stripe;
// otherwise non-production falls back to the fake provider and production
// fails closed (the catalog-store pattern).
func NewProviderFromEnvironment(ctx context.Context) (Provider, error) {
	return NewProviderFromEnvironmentWithAWSConfigLoader(ctx, awsconfig.NewLoader())
}

func NewProviderFromEnvironmentWithAWSConfigLoader(ctx context.Context, configLoader awsconfig.Loader) (Provider, error) {
	switch explicit := strings.ToLower(strings.TrimSpace(os.Getenv(EnvPaymentsProvider))); explicit {
	case KindStripe:
		return stripeProviderFromEnvironment(ctx, configLoader)
	case KindFake:
		if appenv.IsProduction() {
			return nil, ErrFakeProviderNotAllowedInProduction
		}
		return NewFakeProvider(), nil
	case "":
	default:
		return nil, fmt.Errorf("unsupported %s value %q", EnvPaymentsProvider, explicit)
	}

	if strings.TrimSpace(os.Getenv(EnvStripeCredentialsSecretJSON)) != "" || strings.TrimSpace(os.Getenv(EnvStripeCredentialsSecretName)) != "" {
		return stripeProviderFromEnvironment(ctx, configLoader)
	}
	if appenv.IsProduction() {
		return nil, ErrPaymentsProviderNotConfigured
	}

	return NewFakeProvider(), nil
}

// stripeProviderFromEnvironment keeps a failed construction from leaking a
// typed-nil *StripeProvider into the Provider interface return.
func stripeProviderFromEnvironment(ctx context.Context, configLoader awsconfig.Loader) (Provider, error) {
	provider, err := NewStripeProviderFromEnvironmentWithAWSConfigLoader(ctx, configLoader)
	if err != nil {
		return nil, err
	}

	return provider, nil
}
