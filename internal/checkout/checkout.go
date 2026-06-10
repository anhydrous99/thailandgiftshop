// Package checkout orchestrates order placement, payment finalization, and
// webhook handling across the commerce store, the payment provider, and the
// catalog stock store. It owns no HTTP and no templates: the ssr and admin
// handlers call into it and translate its results into responses.
package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
)

// Order math fixed for v1: shipping is free and tax is not collected, so the
// total always equals the line-item subtotal, charged in USD cents.
const orderCurrency = "usd"

// paymentSessionTTL bounds how long a hosted checkout session stays payable.
// Stripe guarantees a checkout.session.expired webhook for every session that
// reaches this deadline unfinished, which is what releases reserved stock.
const paymentSessionTTL = 30 * time.Minute

// cartUpdateAttempts caps version-conflict retries on cart pointer writes.
const cartUpdateAttempts = 3

// releaseClaimAttempts caps version-conflict retries on the stock-release
// claim patch (ClaimAndReleaseOrderStock).
const releaseClaimAttempts = 3

var (
	// ErrOrderNotFinalizable reports a paid session for an order already in a
	// terminal non-paid status: money moved but the order can never reconcile,
	// so webhook retries cannot fix it (§7.2/§7.3).
	ErrOrderNotFinalizable = errors.New("checkout: order is in a terminal status and cannot be finalized")
	// ErrOrderPaidNotCanceled reports that a cancel found the order's checkout
	// session already paid, so the order was finalized to paid instead of
	// being canceled.
	ErrOrderPaidNotCanceled = errors.New("checkout: order was paid and finalized instead of canceled")
	// ErrPaymentSessionInFlight reports that the order's checkout session has
	// completed but its asynchronous payment has not settled: the provider
	// refuses to expire it, so the order must stay pending until a webhook
	// resolves the payment either way.
	ErrPaymentSessionInFlight = errors.New("checkout: payment session completed with payment still in flight")
	// ErrStockReleaseFailed wraps a reserved-stock release failure that
	// happened after the order's terminal transition committed. The release
	// claim was returned best-effort, so a retry (Stripe redelivery, another
	// admin cancel) re-attempts the release.
	ErrStockReleaseFailed = errors.New("checkout: stock release failed")
)

const (
	successURLTemplate = "/checkout/confirm?session_id={CHECKOUT_SESSION_ID}"
	cancelURLPath      = "/checkout?canceled=1"
	confirmURLPath     = "/checkout/confirm"
)

const (
	paymentStatusPaid   = "paid"
	paymentStatusUnpaid = "unpaid"

	sessionModeSetup = "setup"
)

// Webhook event types consumed from the payment provider (§7.3); everything
// else is acknowledged and ignored.
const (
	eventCheckoutSessionCompleted             = "checkout.session.completed"
	eventCheckoutSessionAsyncPaymentSucceeded = "checkout.session.async_payment_succeeded"
	eventCheckoutSessionAsyncPaymentFailed    = "checkout.session.async_payment_failed"
	eventCheckoutSessionExpired               = "checkout.session.expired"
)

// CheckoutPayment metric outcomes.
const (
	outcomePaymentSuccess    = "success"
	outcomeInsufficientStock = "insufficient_stock"
	outcomeProviderError     = "provider_error"
	outcomePaymentError      = "error"
)

// StripeWebhook metric outcomes recorded here; invalid_signature is recorded
// by the ssr webhook handler before ApplyWebhookEvent is reached.
const (
	outcomeWebhookProcessed         = "processed"
	outcomeWebhookIgnored           = "ignored"
	outcomeWebhookAmountMismatch    = "amount_mismatch"
	outcomeWebhookPaidAfterTerminal = "paid_after_terminal"
	outcomeWebhookError             = "error"
)

// checkoutLogger writes structured JSON lines to stdout, where the Lambda
// runtime forwards them to CloudWatch Logs alongside the EMF metric records.
var checkoutLogger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

// Service wires the checkout orchestration dependencies. BaseURL is the
// absolute public origin used to build success/cancel URLs; Now is the
// injected clock (nil falls back to time.Now, the admin precedent).
type Service struct {
	Commerce commerce.Store
	Payments payments.Provider
	Stock    catalog.StockStore
	Metrics  observability.Recorder
	BaseURL  string
	Now      func() time.Time
}

// PlaceOrderInput carries everything PlaceOrder needs, resolved by the ssr
// handler beforehand: the signed-in customer, the shipping address snapshot
// (plus its ID for fingerprinting), the normalized, priced order lines, and
// the customer's current server cart record.
type PlaceOrderInput struct {
	Customer  commerce.Customer
	Email     string
	AddressID string
	Address   commerce.OrderAddress
	Lines     []commerce.OrderLine
	Cart      commerce.CartRecord
}

// PlaceOrder runs the §7.1 sequence: idempotent re-entry through the cart's
// pending-order pointer, stock reservation, order creation before any money
// moves, lazy provider-customer creation, hosted checkout session creation
// with compensation on provider failure, the session-id patch, and the cart
// pointer update. It returns the URL the shopper should be redirected to.
func (s *Service) PlaceOrder(ctx context.Context, input PlaceOrderInput) (string, commerce.Order, error) {
	if input.Customer.ID == "" {
		return "", commerce.Order{}, errors.New("checkout: place order requires a customer")
	}
	if len(input.Lines) == 0 {
		return "", commerce.Order{}, errors.New("checkout: place order requires at least one line")
	}

	fingerprint := s.Fingerprint(input.Lines, input.AddressID)
	cartRecord := input.Cart
	cartRecord.CustomerID = input.Customer.ID

	// Step 2: idempotent re-entry via the cart's pending-order pointer.
	if cartRecord.PendingOrderID != "" {
		redirectURL, pending, done, err := s.resumePendingOrder(ctx, cartRecord.PendingOrderID, fingerprint)
		if err != nil {
			return "", commerce.Order{}, err
		}
		if done {
			return redirectURL, pending, nil
		}
		cartRecord.PendingOrderID = ""
		cartRecord.PendingFingerprint = ""
	}

	// Step 3: reserve stock before the order exists; an insufficient-stock
	// failure surfaces as a typed catalog.InsufficientStockError for the ssr
	// re-render path.
	if err := reserveStockForOrder(ctx, s.Stock, input.Lines); err != nil {
		var insufficient catalog.InsufficientStockError
		if errors.As(err, &insufficient) {
			s.recordCheckoutPayment(outcomeInsufficientStock)
		} else {
			s.recordCheckoutPayment(outcomePaymentError)
		}
		return "", commerce.Order{}, err
	}

	// Step 4: the order exists before money moves.
	subtotal := 0
	for _, line := range input.Lines {
		subtotal += line.LineTotalCents
	}
	created, err := s.Commerce.CreateOrder(ctx, commerce.Order{
		CustomerID:      input.Customer.ID,
		Email:           input.Email,
		Status:          commerce.OrderStatusPendingPayment,
		Lines:           slices.Clone(input.Lines),
		SubtotalCents:   subtotal,
		ShippingCents:   0,
		TaxCents:        0,
		TotalCents:      subtotal,
		Currency:        orderCurrency,
		ShippingAddress: input.Address,
		CartFingerprint: fingerprint,
		CheckoutAttempt: 1,
	})
	if err != nil {
		s.releaseReservedLines(ctx, input.Lines, "create order failed")
		s.recordCheckoutPayment(outcomePaymentError)
		return "", commerce.Order{}, err
	}

	// Step 5: lazy provider-customer creation; a SetStripeCustomerID race is
	// resolved by adopting whatever ID won the conditional write.
	stripeCustomerID := input.Customer.StripeCustomerID
	if stripeCustomerID == "" {
		providerCustomerID, err := s.Payments.EnsureCustomer(ctx, input.Customer.ID, input.Email)
		if err != nil {
			s.compensatePlaceOrder(ctx, created, "ensure provider customer failed")
			s.recordCheckoutPayment(outcomeProviderError)
			return "", commerce.Order{}, err
		}
		winner, err := s.Commerce.SetStripeCustomerID(ctx, input.Customer.ID, providerCustomerID)
		if err != nil {
			s.compensatePlaceOrder(ctx, created, "persist provider customer id failed")
			s.recordCheckoutPayment(outcomePaymentError)
			return "", commerce.Order{}, err
		}
		stripeCustomerID = winner.StripeCustomerID
	}

	// Step 6: hosted checkout session; provider failure compensates by
	// releasing stock and canceling the order before money could move.
	paymentSession, err := s.Payments.CreatePaymentSession(ctx, payments.PaymentSessionInput{
		OrderID:          created.ID,
		CustomerID:       created.CustomerID,
		StripeCustomerID: stripeCustomerID,
		Email:            input.Email,
		Lines:            sessionLines(created.Lines),
		TotalCents:       created.TotalCents,
		SuccessURL:       s.baseURL() + successURLTemplate,
		CancelURL:        s.baseURL() + cancelURLPath,
		ExpiresAt:        s.now().Add(paymentSessionTTL),
		Attempt:          created.CheckoutAttempt,
	})
	if err != nil {
		s.compensatePlaceOrder(ctx, created, "create payment session failed")
		s.recordCheckoutPayment(outcomeProviderError)
		return "", commerce.Order{}, err
	}

	// Step 7: record the session ID with a same-status versioned patch, not a
	// status transition.
	patched, err := s.Commerce.PatchOrder(ctx, created.ID, commerce.OrderStatusPendingPayment, created.Version, commerce.OrderPatch{
		Actor:                   commerce.OrderActorCustomer,
		StripeCheckoutSessionID: &paymentSession.ID,
	})
	if err != nil {
		// The session exists at the provider even though the patch failed;
		// carry its ID so the compensation can expire it.
		created.StripeCheckoutSessionID = paymentSession.ID
		s.compensatePlaceOrder(ctx, created, "record checkout session id failed")
		s.recordCheckoutPayment(outcomePaymentError)
		return "", commerce.Order{}, err
	}

	// Step 8: point the cart at the pending order. The cart lines are NOT
	// cleared; a canceled payment returns the shopper to an intact cart.
	concurrentOrderID, err := s.pointCartAtOrder(ctx, cartRecord, patched.ID, fingerprint)
	if err != nil {
		s.compensatePlaceOrder(ctx, patched, "cart pending pointer update failed")
		s.recordCheckoutPayment(outcomePaymentError)
		return "", commerce.Order{}, err
	}
	if concurrentOrderID != "" {
		// A concurrent place-order won the cart pointer: re-entry semantics.
		// This request's fresh order is canceled (session expired, stock
		// released) and the shopper is sent to the winner's session.
		redirectURL, winner, err := s.adoptConcurrentPendingOrder(ctx, concurrentOrderID, patched)
		if err != nil {
			s.recordCheckoutPayment(outcomePaymentError)
			return "", commerce.Order{}, err
		}
		return redirectURL, winner, nil
	}

	return paymentSession.URL, patched, nil
}

// resumePendingOrder implements the §7.1 step-2 re-entry. done=true means
// PlaceOrder should stop and return redirectURL/order as-is; done=false means
// the pointer was stale (the order was canceled and its stock released where
// applicable) and a fresh order should be placed.
func (s *Service) resumePendingOrder(ctx context.Context, pendingOrderID string, fingerprint string) (string, commerce.Order, bool, error) {
	pending, found, err := s.Commerce.GetOrder(ctx, pendingOrderID)
	if err != nil {
		s.recordCheckoutPayment(outcomePaymentError)
		return "", commerce.Order{}, false, err
	}
	if !found {
		return "", commerce.Order{}, false, nil
	}

	if pending.Status == commerce.OrderStatusPendingPayment && pending.CartFingerprint == fingerprint && pending.StripeCheckoutSessionID != "" {
		paymentSession, err := s.Payments.GetSession(ctx, pending.StripeCheckoutSessionID)
		switch {
		case errors.Is(err, payments.ErrSessionNotFound):
			// Stale session: fall through to cancel + fresh order.
		case err != nil:
			s.recordCheckoutPayment(outcomeProviderError)
			return "", commerce.Order{}, false, err
		case paymentSession.PaymentStatus == paymentStatusPaid:
			// Payment already landed (webhook latency): route the shopper
			// through the real confirm reconcile path instead of canceling a
			// paid-for order.
			return s.confirmURL(paymentSession.ID), pending, true, nil
		case paymentSession.PaymentStatus == paymentStatusUnpaid && paymentSession.URL != "":
			// Session still open: same order, same reservation, same URL.
			return paymentSession.URL, pending, true, nil
		}
	}

	if pending.Status == commerce.OrderStatusPendingPayment || orderStockReleased(pending.Status) {
		// Fingerprint mismatch or unusable session: cancel and release before
		// reserving for the replacement order. Stale pointers to orders
		// already in a stock-released status take the cancel's replay path,
		// which re-attempts an unreleased claim (a previous cancel may have
		// committed the transition and then failed the release).
		if err := s.CancelPendingOrder(ctx, pending); err != nil {
			if errors.Is(err, ErrOrderPaidNotCanceled) {
				// The stale order's payment landed while we were canceling:
				// it was finalized (cart + pointer cleared) and a fresh order
				// is placed for the current lines.
				return "", commerce.Order{}, false, nil
			}
			s.recordCheckoutPayment(outcomePaymentError)
			return "", commerce.Order{}, false, err
		}
	}

	return "", commerce.Order{}, false, nil
}

// adoptConcurrentPendingOrder resolves a lost cart-pointer race in PlaceOrder
// step 8: the fresh order this request just created is canceled through the
// session-safe cancel, and the shopper is redirected to the concurrent
// winner's still-open session (or its confirm path when it already paid).
func (s *Service) adoptConcurrentPendingOrder(ctx context.Context, winnerOrderID string, fresh commerce.Order) (string, commerce.Order, error) {
	s.compensatePlaceOrder(ctx, fresh, "lost cart pointer race")

	winner, found, err := s.Commerce.GetOrder(ctx, winnerOrderID)
	if err != nil {
		return "", commerce.Order{}, err
	}
	if !found || winner.Status != commerce.OrderStatusPendingPayment || winner.StripeCheckoutSessionID == "" {
		return "", commerce.Order{}, fmt.Errorf("checkout: concurrent pending order %q is not resumable", winnerOrderID)
	}
	paymentSession, err := s.Payments.GetSession(ctx, winner.StripeCheckoutSessionID)
	if err != nil {
		return "", commerce.Order{}, err
	}
	switch {
	case paymentSession.PaymentStatus == paymentStatusPaid:
		return s.confirmURL(paymentSession.ID), winner, nil
	case paymentSession.URL != "":
		return paymentSession.URL, winner, nil
	}

	return "", commerce.Order{}, fmt.Errorf("checkout: concurrent pending order %q has no open session", winnerOrderID)
}

// FinalizePayment is the §7.4 reconcile shared by the confirm page and the
// webhook: verify the session against the order, transition to paid with the
// payment patch, and clear the server cart plus its pending pointer. Replays
// are no-ops that still re-attempt the idempotent cart cleanup.
func (s *Service) FinalizePayment(ctx context.Context, orderID string, paymentSession payments.Session) (commerce.Order, error) {
	order, found, err := s.Commerce.GetOrder(ctx, orderID)
	if err != nil {
		s.recordCheckoutPayment(outcomePaymentError)
		return commerce.Order{}, err
	}
	if !found {
		return commerce.Order{}, fmt.Errorf("checkout: order %q not found", orderID)
	}

	switch order.Status {
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered:
		// Replay: never downgrade, but re-attempt the cart cleanup in case an
		// earlier finalize crashed between the transition and the cart write.
		if err := s.clearCartAfterPayment(ctx, order); err != nil {
			return order, err
		}
		return order, nil
	case commerce.OrderStatusPendingPayment:
	default:
		return order, fmt.Errorf("%w: order %q is %s", ErrOrderNotFinalizable, orderID, order.Status)
	}

	if paymentSession.OrderID != orderID {
		return order, fmt.Errorf("checkout: payment session %q belongs to order %q, not %q", paymentSession.ID, paymentSession.OrderID, orderID)
	}
	if paymentSession.PaymentStatus != paymentStatusPaid {
		return order, fmt.Errorf("checkout: payment session %q is %q, not paid", paymentSession.ID, paymentSession.PaymentStatus)
	}

	// Webhook payloads carry the payment intent as a bare ID with no card
	// fields; fetch the expanded session for the display patch. Best-effort:
	// brand/last4 are cosmetic and must never block a paid order.
	paymentSession = s.expandSessionCard(ctx, paymentSession)

	paidAt := s.now()
	patch := commerce.OrderPatch{Actor: commerce.OrderActorStripe, PaidAt: &paidAt}
	if paymentSession.PaymentIntentID != "" {
		patch.StripePaymentIntentID = &paymentSession.PaymentIntentID
	}
	if paymentSession.CardBrand != "" {
		patch.PaymentCardBrand = &paymentSession.CardBrand
	}
	if paymentSession.CardLast4 != "" {
		patch.PaymentCardLast4 = &paymentSession.CardLast4
	}

	updated, err := s.Commerce.TransitionOrder(ctx, orderID, commerce.OrderStatusPendingPayment, commerce.OrderStatusPaid, patch)
	if err != nil {
		if errors.Is(err, commerce.ErrOrderTransitionConflict) {
			current, stillFound, getErr := s.Commerce.GetOrder(ctx, orderID)
			if getErr == nil && stillFound && orderIsPaidOrLater(current.Status) {
				// Lost the race to another finalize: success either way.
				if cartErr := s.clearCartAfterPayment(ctx, current); cartErr != nil {
					return current, cartErr
				}
				return current, nil
			}
		}
		s.recordCheckoutPayment(outcomePaymentError)
		return order, err
	}

	s.recordCheckoutPayment(outcomePaymentSuccess)
	if err := s.clearCartAfterPayment(ctx, updated); err != nil {
		return updated, err
	}

	return updated, nil
}

// ApplyWebhookEvent handles a signature-verified provider event per §7.3.
// Returning nil means the caller should acknowledge with a 200; returning an
// error means a transient failure the provider should retry (500).
func (s *Service) ApplyWebhookEvent(ctx context.Context, event payments.Event) error {
	switch event.Type {
	case eventCheckoutSessionCompleted,
		eventCheckoutSessionAsyncPaymentSucceeded,
		eventCheckoutSessionAsyncPaymentFailed,
		eventCheckoutSessionExpired:
	default:
		s.recordStripeWebhook(outcomeWebhookIgnored)
		return nil
	}

	// A completed mode=setup session is the save-a-card flow: acknowledge
	// and ignore.
	if event.Type == eventCheckoutSessionCompleted && event.Session.Mode == sessionModeSetup {
		if err := s.markEventProcessed(ctx, event); err != nil {
			s.recordStripeWebhook(outcomeWebhookError)
			return err
		}
		s.recordStripeWebhook(outcomeWebhookIgnored)
		return nil
	}

	if event.OrderID == "" {
		checkoutLogger.Warn("stripe webhook event carries no order id",
			slog.String("event_id", event.ID),
			slog.String("event_type", event.Type),
		)
		s.recordStripeWebhook(outcomeWebhookIgnored)
		return nil
	}

	order, found, err := s.Commerce.GetOrder(ctx, event.OrderID)
	if err != nil {
		s.recordStripeWebhook(outcomeWebhookError)
		return err
	}
	if !found {
		checkoutLogger.Warn("stripe webhook event references an unknown order",
			slog.String("event_id", event.ID),
			slog.String("event_type", event.Type),
			slog.String("order_id", event.OrderID),
		)
		s.recordStripeWebhook(outcomeWebhookIgnored)
		return nil
	}

	switch event.Type {
	case eventCheckoutSessionCompleted, eventCheckoutSessionAsyncPaymentSucceeded:
		if event.Type == eventCheckoutSessionCompleted && event.Session.PaymentStatus != paymentStatusPaid {
			// Asynchronous payment still pending; the async_payment_succeeded
			// or async_payment_failed event will follow.
			if err := s.markEventProcessed(ctx, event); err != nil {
				s.recordStripeWebhook(outcomeWebhookError)
				return err
			}
			s.recordStripeWebhook(outcomeWebhookIgnored)
			return nil
		}
		if order.Status == commerce.OrderStatusPendingPayment && event.Session.AmountTotalCents != order.TotalCents {
			// Cross-check failed: log + alarmed metric, leave the order
			// pending, no transition, and acknowledge so the provider does
			// not retry an event that can never reconcile.
			checkoutLogger.Error("stripe webhook amount does not match the order total",
				slog.String("event_id", event.ID),
				slog.String("event_type", event.Type),
				slog.String("order_id", order.ID),
				slog.Int("amount_total_cents", event.Session.AmountTotalCents),
				slog.Int("order_total_cents", order.TotalCents),
			)
			s.recordStripeWebhook(outcomeWebhookAmountMismatch)
			return nil
		}
		if _, err := s.FinalizePayment(ctx, order.ID, event.Session); err != nil {
			if errors.Is(err, ErrOrderNotFinalizable) {
				// Money moved for an order already in a terminal status: a
				// retry can never reconcile it (§7.2/§7.3), so log the ids
				// the manual-refund runbook needs, record the alarmed
				// outcome, and acknowledge to stop the retries.
				checkoutLogger.Error("stripe webhook delivered a paid session for a terminal order; refund manually",
					slog.String("event_id", event.ID),
					slog.String("event_type", event.Type),
					slog.String("order_id", order.ID),
					slog.String("order_status", string(order.Status)),
					slog.String("payment_intent_id", event.Session.PaymentIntentID),
				)
				s.recordStripeWebhook(outcomeWebhookPaidAfterTerminal)
				return nil
			}
			s.recordStripeWebhook(outcomeWebhookError)
			return err
		}
	case eventCheckoutSessionAsyncPaymentFailed:
		if err := s.failPendingOrder(ctx, order, commerce.OrderStatusPaymentFailed, event); err != nil {
			s.recordStripeWebhook(outcomeWebhookError)
			return err
		}
	case eventCheckoutSessionExpired:
		if err := s.failPendingOrder(ctx, order, commerce.OrderStatusExpired, event); err != nil {
			s.recordStripeWebhook(outcomeWebhookError)
			return err
		}
	}

	if err := s.markEventProcessed(ctx, event); err != nil {
		s.recordStripeWebhook(outcomeWebhookError)
		return err
	}
	s.recordStripeWebhook(outcomeWebhookProcessed)

	return nil
}

// failPendingOrder moves a pending order to a payment-failure terminal status
// (payment_failed or expired) and releases its reserved stock through the
// claim protocol. Replays for an order already in the target status re-attempt
// an unreleased claim (an earlier delivery may have committed the transition
// and then failed the release); paid is never downgraded.
func (s *Service) failPendingOrder(ctx context.Context, order commerce.Order, target commerce.OrderStatus, event payments.Event) error {
	switch order.Status {
	case target:
		return s.ClaimAndReleaseOrderStock(ctx, order.ID)
	case commerce.OrderStatusPendingPayment:
	default:
		checkoutLogger.Warn("stripe webhook skipped a transition on a non-pending order",
			slog.String("event_id", event.ID),
			slog.String("event_type", event.Type),
			slog.String("order_id", order.ID),
			slog.String("order_status", string(order.Status)),
			slog.String("target_status", string(target)),
		)
		return nil
	}

	if _, err := s.Commerce.TransitionOrder(ctx, order.ID, commerce.OrderStatusPendingPayment, target, commerce.OrderPatch{Actor: commerce.OrderActorStripe}); err != nil {
		if errors.Is(err, commerce.ErrOrderTransitionConflict) {
			current, found, getErr := s.Commerce.GetOrder(ctx, order.ID)
			if getErr == nil && found && current.Status != commerce.OrderStatusPendingPayment {
				if orderStockReleased(current.Status) {
					// Someone else moved the order to a stock-releasing
					// status; recover their release if it never completed.
					return s.ClaimAndReleaseOrderStock(ctx, current.ID)
				}
				// Paid won the race: the reservation stays.
				return nil
			}
		}
		return err
	}

	return s.ClaimAndReleaseOrderStock(ctx, order.ID)
}

// CancelPendingOrder transitions a pending_payment order to canceled (actor
// system) and releases its reserved stock; see CancelPendingOrderAs.
func (s *Service) CancelPendingOrder(ctx context.Context, order commerce.Order) error {
	return s.CancelPendingOrderAs(ctx, order, commerce.OrderActorSystem)
}

// CancelPendingOrderAs cancels a pending_payment order, recording the given
// status-history actor, and releases its reserved stock through the claim
// protocol. Before the transition the order's checkout session is shut down
// conservatively (expirePaymentSessionForCancel): an already-paid session
// finalizes the order instead (ErrOrderPaidNotCanceled) and a completed
// session with its payment still settling refuses the cancel
// (ErrPaymentSessionInFlight). Stock-released terminal statuses re-attempt an
// unreleased claim; canceling a paid/shipped/delivered order is refused.
func (s *Service) CancelPendingOrderAs(ctx context.Context, order commerce.Order, actor string) error {
	switch order.Status {
	case commerce.OrderStatusCanceled, commerce.OrderStatusExpired, commerce.OrderStatusPaymentFailed:
		// Replay: the transition already committed, but the release may not
		// have completed; re-attempt an unreleased claim.
		return s.ClaimAndReleaseOrderStock(ctx, order.ID)
	case commerce.OrderStatusPendingPayment:
	default:
		return fmt.Errorf("checkout: cannot cancel order %q in status %s", order.ID, order.Status)
	}

	if err := s.expirePaymentSessionForCancel(ctx, order); err != nil {
		return err
	}

	if _, err := s.Commerce.TransitionOrder(ctx, order.ID, commerce.OrderStatusPendingPayment, commerce.OrderStatusCanceled, commerce.OrderPatch{Actor: actor}); err != nil {
		if errors.Is(err, commerce.ErrOrderTransitionConflict) {
			current, found, getErr := s.Commerce.GetOrder(ctx, order.ID)
			if getErr == nil && found && orderStockReleased(current.Status) {
				// Lost the race to another canceler/expirer; recover its
				// release if it never completed.
				return s.ClaimAndReleaseOrderStock(ctx, current.ID)
			}
		}
		return err
	}

	return s.ClaimAndReleaseOrderStock(ctx, order.ID)
}

// expirePaymentSessionForCancel shuts down an order's hosted checkout session
// before the order is canceled, so a shopper can never pay a canceled order
// from a stale tab. Returning nil means the cancel may proceed: the session
// is gone, already expired, or was just expired here. A paid session is
// finalized instead (ErrOrderPaidNotCanceled); a completed-but-unpaid session
// (asynchronous payment in flight — the provider refuses to expire it) leaves
// the order pending (ErrPaymentSessionInFlight).
func (s *Service) expirePaymentSessionForCancel(ctx context.Context, order commerce.Order) error {
	if order.StripeCheckoutSessionID == "" || s.Payments == nil {
		return nil
	}

	paymentSession, err := s.Payments.GetSession(ctx, order.StripeCheckoutSessionID)
	if errors.Is(err, payments.ErrSessionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case paymentSession.PaymentStatus == paymentStatusPaid:
		return s.finalizeInsteadOfCancel(ctx, order, paymentSession)
	case paymentSession.Status == payments.SessionStatusExpired:
		return nil
	case paymentSession.Status == payments.SessionStatusComplete:
		return fmt.Errorf("%w: session %q for order %q", ErrPaymentSessionInFlight, paymentSession.ID, order.ID)
	}

	expireErr := s.Payments.ExpireSession(ctx, order.StripeCheckoutSessionID)
	if expireErr == nil || errors.Is(expireErr, payments.ErrSessionNotFound) {
		return nil
	}
	// The provider refused: the session may have completed or expired between
	// the read and the expire call. Re-read to classify; an unclassifiable
	// refusal propagates so the order stays pending (safe: never cancel an
	// order whose session might still collect money).
	current, getErr := s.Payments.GetSession(ctx, order.StripeCheckoutSessionID)
	if getErr == nil {
		switch {
		case current.PaymentStatus == paymentStatusPaid:
			return s.finalizeInsteadOfCancel(ctx, order, current)
		case current.Status == payments.SessionStatusExpired:
			return nil
		case current.Status == payments.SessionStatusComplete:
			return fmt.Errorf("%w: session %q for order %q", ErrPaymentSessionInFlight, current.ID, order.ID)
		}
	}

	return expireErr
}

// finalizeInsteadOfCancel reconciles a cancel attempt that discovered the
// session already paid: never cancel paid-for goods — finalize and report
// ErrOrderPaidNotCanceled so the caller can adjust.
func (s *Service) finalizeInsteadOfCancel(ctx context.Context, order commerce.Order, paymentSession payments.Session) error {
	if _, err := s.FinalizePayment(ctx, order.ID, paymentSession); err != nil {
		return err
	}
	return fmt.Errorf("%w: order %q", ErrOrderPaidNotCanceled, order.ID)
}

// ClaimAndReleaseOrderStock releases an order's reserved stock exactly once
// across concurrent and replayed terminal transitions, using the persisted
// StockReleasedAt marker as the claim. Every release site (webhook failures,
// pending-order cancels, place-order compensation, admin cancel) goes through
// here. Protocol: re-read; marker set ⇒ done; claim it with a versioned
// patch (conflict ⇒ re-read and retry, where a now-set marker ⇒ done); the
// claim winner releases the stock; a release failure unclaims best-effort and
// returns ErrStockReleaseFailed so the caller's retry re-attempts.
//
// Accepted window: a crash between the committed claim patch and the stock
// release leaves the marker set with the stock still reserved — the same
// exposure as a crash inside any non-transactional two-step, traded for
// replay/race safety. It is recoverable only by manual stock repair.
func (s *Service) ClaimAndReleaseOrderStock(ctx context.Context, orderID string) error {
	for attempt := 0; attempt < releaseClaimAttempts; attempt++ {
		order, found, err := s.Commerce.GetOrder(ctx, orderID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("checkout: order %q not found for stock release", orderID)
		}
		if !order.StockReleasedAt.IsZero() {
			// Already claimed (and released, modulo the crash window above).
			return nil
		}
		if !orderStockReleased(order.Status) {
			return fmt.Errorf("checkout: order %q is %s; its reservation is not releasable", orderID, order.Status)
		}

		claimedAt := s.now()
		claimed, err := s.Commerce.PatchOrder(ctx, orderID, order.Status, order.Version, commerce.OrderPatch{StockReleasedAt: &claimedAt})
		if err != nil {
			if errors.Is(err, commerce.ErrVersionConflict) {
				continue
			}
			return err
		}

		if releaseErr := ReleaseStockForOrder(ctx, s.Stock, claimed); releaseErr != nil {
			// Unclaim best-effort so a retry can re-drive the release; an
			// unclaim failure leaves the marker set inside the accepted
			// crash window documented above.
			zero := time.Time{}
			if _, unclaimErr := s.Commerce.PatchOrder(ctx, orderID, claimed.Status, claimed.Version, commerce.OrderPatch{StockReleasedAt: &zero}); unclaimErr != nil {
				checkoutLogger.Error("checkout could not unclaim a failed stock release",
					slog.String("order_id", orderID),
					slog.String("release_error", releaseErr.Error()),
					slog.String("unclaim_error", unclaimErr.Error()),
				)
			}
			return fmt.Errorf("%w: order %q: %w", ErrStockReleaseFailed, orderID, releaseErr)
		}
		return nil
	}

	return fmt.Errorf("checkout: stock release claim for order %q failed after %d attempts: %w", orderID, releaseClaimAttempts, commerce.ErrVersionConflict)
}

// Fingerprint returns the lowercase sha256 hex digest of the canonical priced
// lines plus the shipping address ID. Lines are canonicalized by sorting on
// their identity and price fields, so cart ordering never changes the
// fingerprint while any quantity, price, variant, or address change does.
func (s *Service) Fingerprint(lines []commerce.OrderLine, addressID string) string {
	keys := make([]string, 0, len(lines))
	for _, line := range lines {
		keys = append(keys, strings.Join([]string{
			line.ProductID,
			line.VariantID,
			line.Slug,
			strconv.Itoa(line.UnitPriceCents),
			strconv.Itoa(line.Quantity),
		}, "|"))
	}
	sort.Strings(keys)

	digest := sha256.Sum256([]byte(strings.Join(keys, "\n") + "\naddress:" + addressID))
	return hex.EncodeToString(digest[:])
}

// clearCartAfterPayment empties the server cart and clears its pending
// pointer once payment succeeded. The pointer guard keeps a late replay from
// clobbering a cart that a newer checkout already owns; version conflicts are
// re-read and retried because the operation is idempotent.
func (s *Service) clearCartAfterPayment(ctx context.Context, order commerce.Order) error {
	for attempt := 0; attempt < cartUpdateAttempts; attempt++ {
		record, found, err := s.Commerce.GetCart(ctx, order.CustomerID)
		if err != nil {
			return err
		}
		if !found || record.PendingOrderID != order.ID {
			return nil
		}
		record.Lines = nil
		record.PendingOrderID = ""
		record.PendingFingerprint = ""
		if _, err := s.Commerce.PutCart(ctx, record); err != nil {
			if errors.Is(err, commerce.ErrVersionConflict) {
				continue
			}
			return err
		}
		return nil
	}

	return fmt.Errorf("checkout: cart clear for customer %q failed after %d attempts: %w", order.CustomerID, cartUpdateAttempts, commerce.ErrVersionConflict)
}

// pointCartAtOrder persists the pending-order pointer on the cart record. On
// a version conflict the latest record is re-read: when it already points at
// a DIFFERENT pending order, a concurrent place-order won the race and that
// order's ID is returned (never overwrite a live pointer — the caller adopts
// the winner instead); otherwise the pointer is re-applied, preserving
// concurrently mutated lines.
func (s *Service) pointCartAtOrder(ctx context.Context, record commerce.CartRecord, orderID string, fingerprint string) (string, error) {
	for attempt := 0; attempt < cartUpdateAttempts; attempt++ {
		record.PendingOrderID = orderID
		record.PendingFingerprint = fingerprint
		if _, err := s.Commerce.PutCart(ctx, record); err != nil {
			if errors.Is(err, commerce.ErrVersionConflict) {
				latest, found, getErr := s.Commerce.GetCart(ctx, record.CustomerID)
				if getErr != nil {
					return "", getErr
				}
				if found {
					if latest.PendingOrderID != "" && latest.PendingOrderID != orderID {
						return latest.PendingOrderID, nil
					}
					record = latest
				} else {
					record = commerce.CartRecord{CustomerID: record.CustomerID}
				}
				continue
			}
			return "", err
		}
		return "", nil
	}

	return "", fmt.Errorf("checkout: cart pointer update for customer %q failed after %d attempts: %w", record.CustomerID, cartUpdateAttempts, commerce.ErrVersionConflict)
}

// compensatePlaceOrder unwinds a partially placed order after a later step
// failed: transition to canceled, then release the reserved stock. Money
// never moved, so a compensation failure is logged for manual stock repair
// rather than escalated past the original error.
func (s *Service) compensatePlaceOrder(ctx context.Context, order commerce.Order, reason string) {
	if err := s.CancelPendingOrder(ctx, order); err != nil {
		checkoutLogger.Error("checkout compensation failed",
			slog.String("service", "checkout"),
			slog.String("order_id", order.ID),
			slog.String("reason", reason),
			slog.String("error", err.Error()),
		)
	}
}

// releaseReservedLines best-effort releases a reservation made before the
// order row existed (CreateOrder failure).
func (s *Service) releaseReservedLines(ctx context.Context, lines []commerce.OrderLine, reason string) {
	if err := ReleaseStockForOrder(ctx, s.Stock, commerce.Order{Lines: lines}); err != nil {
		checkoutLogger.Error("checkout stock release failed",
			slog.String("service", "checkout"),
			slog.String("reason", reason),
			slog.String("error", err.Error()),
		)
	}
}

// expandSessionCard fills the payment intent ID and card display fields from
// the provider when the supplied session lacks them (webhook payloads carry
// the payment intent as a bare ID). Failures are logged and tolerated.
func (s *Service) expandSessionCard(ctx context.Context, paymentSession payments.Session) payments.Session {
	if paymentSession.ID == "" {
		return paymentSession
	}
	if paymentSession.PaymentIntentID != "" && paymentSession.CardBrand != "" && paymentSession.CardLast4 != "" {
		return paymentSession
	}

	expanded, err := s.Payments.GetSession(ctx, paymentSession.ID)
	if err != nil {
		checkoutLogger.Warn("checkout could not expand the payment session card details",
			slog.String("session_id", paymentSession.ID),
			slog.String("error", err.Error()),
		)
		return paymentSession
	}
	if paymentSession.PaymentIntentID == "" {
		paymentSession.PaymentIntentID = expanded.PaymentIntentID
	}
	if paymentSession.CardBrand == "" {
		paymentSession.CardBrand = expanded.CardBrand
	}
	if paymentSession.CardLast4 == "" {
		paymentSession.CardLast4 = expanded.CardLast4
	}

	return paymentSession
}

// markEventProcessed writes the dedupe/audit row after successful processing.
// The commerce.Store interface exposes no read-only event getter, so the
// §7.3 GetItem fast-exit is skipped; correctness lives in the idempotent
// conditional transitions, and this row remains optimization + audit.
func (s *Service) markEventProcessed(ctx context.Context, event payments.Event) error {
	if event.ID == "" {
		return nil
	}
	_, err := s.Commerce.MarkStripeEventProcessed(ctx, event.ID, event.Type, event.OrderID)
	return err
}

func (s *Service) baseURL() string {
	return strings.TrimSuffix(s.BaseURL, "/")
}

func (s *Service) confirmURL(sessionID string) string {
	return s.baseURL() + confirmURLPath + "?session_id=" + url.QueryEscape(sessionID)
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) recordCheckoutPayment(outcome string) {
	if s.Metrics == nil {
		return
	}
	s.Metrics.Record(observability.Count(
		observability.MetricCheckoutPayment,
		observability.Dim("Service", "checkout"),
		observability.Dim("Outcome", outcome),
	))
}

func (s *Service) recordStripeWebhook(outcome string) {
	if s.Metrics == nil {
		return
	}
	// Service is "ssr" (not "checkout") because webhook deliveries are served
	// by the SSR Lambda: its handler records invalid_signature under
	// Service=ssr before events reach ApplyWebhookEvent, and the infra
	// StripeWebhook widgets and the alarmed error rollup query the single
	// {Service=ssr, Outcome} dimension set for every outcome.
	s.Metrics.Record(observability.Count(
		observability.MetricStripeWebhook,
		observability.Dim("Service", "ssr"),
		observability.Dim("Outcome", outcome),
	))
}

func orderIsPaidOrLater(status commerce.OrderStatus) bool {
	switch status {
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered:
		return true
	}
	return false
}

// orderStockReleased reports whether a status implies the order's reserved
// stock was already released by whoever moved it there.
func orderStockReleased(status commerce.OrderStatus) bool {
	switch status {
	case commerce.OrderStatusCanceled, commerce.OrderStatusExpired, commerce.OrderStatusPaymentFailed:
		return true
	}
	return false
}

// sessionLines maps the frozen order snapshot onto provider display lines;
// names get the " — " variant suffix the hosted checkout page shows.
func sessionLines(lines []commerce.OrderLine) []payments.SessionLine {
	out := make([]payments.SessionLine, 0, len(lines))
	for _, line := range lines {
		name := line.Name
		if line.VariantLabel != "" {
			name = name + " — " + line.VariantLabel
		}
		out = append(out, payments.SessionLine{
			Name:            name,
			UnitAmountCents: line.UnitPriceCents,
			Quantity:        line.Quantity,
		})
	}
	return out
}
