package payments

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// FakeWebhookSigningSecret signs synthesized webhook payloads for the fake
// provider so tests and curl can exercise the real signature-verification
// path. It is a fixed demo value, never a real Stripe secret.
const FakeWebhookSigningSecret = "whsec_fake_demo_secret"

// fakePayPath is the sentinel on-site URL the ssr handler serves in demo
// mode instead of redirecting to checkout.stripe.com.
const fakePayPath = "/checkout/fake-pay"

const (
	fakeCardBrand       = "visa"
	fakeCardLast4       = "4242"
	fakeCardExpMonth    = 12
	fakeCardExpYear     = 2034
	fakePaymentMethodID = "pm_fake_visa_4242"
)

type fakeSession struct {
	id               string
	mode             string
	orderID          string
	stripeCustomerID string
	paymentStatus    string
	status           string
	paymentIntentID  string
	totalCents       int
	cardBrand        string
	cardLast4        string
	successURL       string
	cancelURL        string
}

func (s *fakeSession) asSession() Session {
	// Mirror Stripe: only open sessions expose a payable URL.
	sessionURL := ""
	if s.status == SessionStatusOpen {
		sessionURL = fakePayPath + "?session_id=" + url.QueryEscape(s.id)
	}
	return Session{
		ID:               s.id,
		URL:              sessionURL,
		OrderID:          s.orderID,
		PaymentIntentID:  s.paymentIntentID,
		PaymentStatus:    s.paymentStatus,
		Status:           s.status,
		AmountTotalCents: s.totalCents,
		CardBrand:        s.cardBrand,
		CardLast4:        s.cardLast4,
		Mode:             s.mode,
	}
}

type fakeRefund struct {
	id              string
	orderID         string
	attempt         int
	paymentIntentID string
	status          string
	amountCents     int
	failureReason   string
}

func (r *fakeRefund) asRefund() Refund {
	return Refund{
		ID:              r.id,
		PaymentIntentID: r.paymentIntentID,
		OrderID:         r.orderID,
		Attempt:         r.attempt,
		Status:          r.status,
		AmountCents:     r.amountCents,
		FailureReason:   r.failureReason,
	}
}

// FakeProvider is the deterministic in-memory provider used for local demo
// runs, CI, and Playwright: zero network, zero keys. The fake-pay page POST
// drives MarkSessionPaid/MarkSetupComplete, after which the real
// /checkout/confirm reconcile path takes over. Refunds settle synchronously
// as succeeded by default; SetNextRefundOutcome and SettleRefund drive the
// pending and failed paths.
type FakeProvider struct {
	mu                sync.Mutex
	sessions          map[string]*fakeSession
	paymentMethods    map[string][]PaymentMethod
	refunds           map[string]*fakeRefund // by refund ID
	refundsByIntent   map[string]string      // payment intent ID -> latest refund ID
	nextRefundStatus  string                 // "" => RefundStatusSucceeded
	nextRefundFailure string
}

var _ Provider = (*FakeProvider)(nil)

func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		sessions:        map[string]*fakeSession{},
		paymentMethods:  map[string][]PaymentMethod{},
		refunds:         map[string]*fakeRefund{},
		refundsByIntent: map[string]string{},
	}
}

func (p *FakeProvider) Kind() string {
	return KindFake
}

func (p *FakeProvider) EnsureCustomer(ctx context.Context, customerID string, email string) (string, error) {
	_ = ctx
	_ = email

	return "cus_fake_" + customerID, nil
}

func (p *FakeProvider) CreatePaymentSession(ctx context.Context, input PaymentSessionInput) (Session, error) {
	_ = ctx
	session := &fakeSession{
		id:               "cs_fake_" + input.OrderID,
		mode:             "payment",
		orderID:          input.OrderID,
		stripeCustomerID: input.StripeCustomerID,
		paymentStatus:    "unpaid",
		status:           SessionStatusOpen,
		totalCents:       input.TotalCents,
		successURL:       input.SuccessURL,
		cancelURL:        input.CancelURL,
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions[session.id] = session

	return session.asSession(), nil
}

func (p *FakeProvider) CreateSetupSession(ctx context.Context, input SetupSessionInput) (Session, error) {
	_ = ctx
	session := &fakeSession{
		id:               "cs_fake_setup_" + input.CustomerID,
		mode:             "setup",
		stripeCustomerID: input.StripeCustomerID,
		paymentStatus:    "no_payment_required",
		status:           SessionStatusOpen,
		successURL:       input.SuccessURL,
		cancelURL:        input.CancelURL,
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions[session.id] = session

	return session.asSession(), nil
}

func (p *FakeProvider) GetSession(ctx context.Context, sessionID string) (Session, error) {
	_ = ctx

	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.sessions[sessionID]
	if !ok {
		return Session{}, ErrSessionNotFound
	}

	return session.asSession(), nil
}

// MarkSessionPaid is driven by the fake-pay page POST. It marks the payment
// session paid with the fixed demo card, optionally saves that card for the
// session's customer, and returns the resolved success URL (with
// {CHECKOUT_SESSION_ID} substituted) the handler should redirect to. Expired
// sessions can never collect money (the Stripe contract checkout relies on
// when it cancels an order); replays on a completed session stay idempotent.
func (p *FakeProvider) MarkSessionPaid(sessionID string, saveCard bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.sessions[sessionID]
	if !ok || session.mode != "payment" {
		return "", ErrSessionNotFound
	}
	if session.status == SessionStatusExpired {
		return "", ErrSessionExpired
	}

	session.status = SessionStatusComplete
	session.paymentStatus = "paid"
	session.paymentIntentID = "pi_fake_" + session.orderID
	session.cardBrand = fakeCardBrand
	session.cardLast4 = fakeCardLast4
	if saveCard {
		p.addFakePaymentMethodLocked(session.stripeCustomerID)
	}

	return resolveSessionURL(session.successURL, session.id), nil
}

// MarkSetupComplete completes a mode=setup session by saving the fixed demo
// card and returns the resolved success URL.
func (p *FakeProvider) MarkSetupComplete(sessionID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.sessions[sessionID]
	if !ok || session.mode != "setup" {
		return "", ErrSessionNotFound
	}
	if session.status == SessionStatusExpired {
		return "", ErrSessionExpired
	}
	session.status = SessionStatusComplete
	p.addFakePaymentMethodLocked(session.stripeCustomerID)

	return resolveSessionURL(session.successURL, session.id), nil
}

// ExpireSession mirrors the Stripe contract: only open sessions can be
// expired, expiring an already-expired session is an idempotent success, and
// expired sessions reject MarkSessionPaid so a canceled order can never be
// paid through a stale fake-pay tab.
func (p *FakeProvider) ExpireSession(ctx context.Context, sessionID string) error {
	_ = ctx

	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	switch session.status {
	case SessionStatusExpired:
		return nil
	case SessionStatusOpen:
		session.status = SessionStatusExpired
		return nil
	}

	return fmt.Errorf("payments: fake session %q is %s and cannot be expired", sessionID, session.status)
}

// SessionRedirectURLs exposes the stored success and cancel URLs (with
// {CHECKOUT_SESSION_ID} substituted) so the fake-pay page can offer both the
// pay and cancel actions without re-deriving them.
func (p *FakeProvider) SessionRedirectURLs(sessionID string) (string, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session, ok := p.sessions[sessionID]
	if !ok {
		return "", "", false
	}

	return resolveSessionURL(session.successURL, session.id), resolveSessionURL(session.cancelURL, session.id), true
}

// SetNextRefundOutcome overrides the status (and failure reason) of the next
// CreateRefund so tests can drive the pending and failed paths; the default
// outcome is an immediately-succeeded refund.
func (p *FakeProvider) SetNextRefundOutcome(status string, failureReason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextRefundStatus = status
	p.nextRefundFailure = failureReason
}

// CreateRefund is deterministic and idempotent: a non-failed refund already
// recorded for the payment intent is returned as-is (Stripe's "a charge can't
// be refunded twice" contract); otherwise a refund with ID
// "re_fake_<orderID>_<attempt>" is created with the configured outcome.
func (p *FakeProvider) CreateRefund(ctx context.Context, input RefundInput) (Refund, error) {
	_ = ctx

	p.mu.Lock()
	defer p.mu.Unlock()
	if input.PaymentIntentID == "" {
		return Refund{}, fmt.Errorf("payments: fake refund requires a payment intent")
	}
	if existingID, ok := p.refundsByIntent[input.PaymentIntentID]; ok {
		if existing, found := p.refunds[existingID]; found && existing.status != RefundStatusFailed {
			return existing.asRefund(), nil
		}
	}

	status := p.nextRefundStatus
	if status == "" {
		status = RefundStatusSucceeded
	}
	failureReason := ""
	if status == RefundStatusFailed {
		failureReason = p.nextRefundFailure
	}
	p.nextRefundStatus = ""
	p.nextRefundFailure = ""

	amountCents := 0
	for _, session := range p.sessions {
		if session.paymentIntentID == input.PaymentIntentID {
			amountCents = session.totalCents
			break
		}
	}

	refund := &fakeRefund{
		id:              fmt.Sprintf("re_fake_%s_%d", input.OrderID, input.Attempt),
		orderID:         input.OrderID,
		attempt:         input.Attempt,
		paymentIntentID: input.PaymentIntentID,
		status:          status,
		amountCents:     amountCents,
		failureReason:   failureReason,
	}
	p.refunds[refund.id] = refund
	p.refundsByIntent[input.PaymentIntentID] = refund.id

	return refund.asRefund(), nil
}

func (p *FakeProvider) GetRefund(ctx context.Context, refundID string) (Refund, error) {
	_ = ctx

	p.mu.Lock()
	defer p.mu.Unlock()
	refund, ok := p.refunds[refundID]
	if !ok {
		return Refund{}, ErrRefundNotFound
	}

	return refund.asRefund(), nil
}

// SettleRefund transitions an existing fake refund to the given status
// (RefundStatusSucceeded/Failed/...), setting failureReason for failed ones —
// the refund-side analogue of MarkSessionPaid. It is how Go tests drive async
// settlement: SetNextRefundOutcome("pending", "") at create time, then
// SettleRefund to flip it before exercising ReconcileRefund or a hand-signed
// refund.updated webhook. Unknown IDs map to ErrRefundNotFound.
func (p *FakeProvider) SettleRefund(refundID string, status string, failureReason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	refund, ok := p.refunds[refundID]
	if !ok {
		return ErrRefundNotFound
	}
	refund.status = status
	refund.failureReason = failureReason

	return nil
}

func (p *FakeProvider) ListPaymentMethods(ctx context.Context, stripeCustomerID string) ([]PaymentMethod, error) {
	_ = ctx

	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]PaymentMethod{}, p.paymentMethods[stripeCustomerID]...), nil
}

func (p *FakeProvider) DetachPaymentMethod(ctx context.Context, stripeCustomerID string, paymentMethodID string) error {
	_ = ctx

	p.mu.Lock()
	defer p.mu.Unlock()
	for index, method := range p.paymentMethods[stripeCustomerID] {
		if method.ID == paymentMethodID {
			methods := p.paymentMethods[stripeCustomerID]
			p.paymentMethods[stripeCustomerID] = append(methods[:index], methods[index+1:]...)
			return nil
		}
	}

	return ErrPaymentMethodNotFound
}

// ParseWebhook verifies a real Stripe-Signature-format HMAC over the raw
// payload against the fixed demo secret, sharing the Stripe provider's
// verification and event-mapping path.
func (p *FakeProvider) ParseWebhook(payload []byte, signatureHeader string, now time.Time) (Event, error) {
	return parseStripeSignedWebhook(payload, signatureHeader, FakeWebhookSigningSecret, now)
}

func (p *FakeProvider) addFakePaymentMethodLocked(stripeCustomerID string) {
	for _, method := range p.paymentMethods[stripeCustomerID] {
		if method.ID == fakePaymentMethodID {
			return
		}
	}
	p.paymentMethods[stripeCustomerID] = append(p.paymentMethods[stripeCustomerID], PaymentMethod{
		ID:       fakePaymentMethodID,
		Brand:    fakeCardBrand,
		Last4:    fakeCardLast4,
		ExpMonth: fakeCardExpMonth,
		ExpYear:  fakeCardExpYear,
	})
}

func resolveSessionURL(value string, sessionID string) string {
	return strings.ReplaceAll(value, "{CHECKOUT_SESSION_ID}", sessionID)
}
