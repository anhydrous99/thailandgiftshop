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
	// ErrOrderNotRefundable reports a refund request for an order whose
	// status is not paid, shipped, delivered, or refund_failed.
	ErrOrderNotRefundable = errors.New("checkout: order status cannot be refunded")
	// ErrRefundNotIssuable reports a refundable order with no payment intent
	// on file; the Stripe dashboard is the only path for it.
	ErrRefundNotIssuable = errors.New("checkout: order has no payment intent to refund")
	// ErrRefundProviderUnavailable reports a nil payments provider; unlike
	// cancel's degraded session-expiry skip, refunds move money and must
	// refuse to proceed without one.
	ErrRefundProviderUnavailable = errors.New("checkout: payments provider unavailable for refund")
)

// errRefundMismatch is the unexported control-flow sentinel applyRefundEvent
// returns when a refund event's payment intent does not match the order; the
// ApplyWebhookEvent caller ACKs it without marking the event processed (the
// amount_mismatch precedent — the event can never reconcile, so do not let
// Stripe retry it).
var errRefundMismatch = errors.New("checkout: refund event payment intent does not match the order")

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
	eventRefundCreated                        = "refund.created"
	eventRefundUpdated                        = "refund.updated"
	eventRefundFailed                         = "refund.failed"
)

// CheckoutPayment metric outcomes.
const (
	outcomePaymentSuccess    = "success"
	outcomeInsufficientStock = "insufficient_stock"
	outcomeProviderError     = "provider_error"
	outcomePaymentError      = "error"
)

// CheckoutRefund metric outcomes.
const (
	outcomeRefundIssued        = "issued"         // admin-initiated provider refund created, order -> refund_pending
	outcomeRefundIssuedAuto    = "issued_auto"    // system-initiated paid_after_terminal auto-refund created
	outcomeRefundSettled       = "settled"        // refund_pending -> refunded
	outcomeRefundFailed        = "failed"         // provider reported the refund failed (alarmed) — admin AND auto-refunds
	outcomeRefundProviderError = "provider_error" // CreateRefund/GetRefund failed (alarmed)
	outcomeRefundError         = "error"          // store/transition failures (alarmed)
)

// StripeWebhook metric outcomes recorded here; invalid_signature is recorded
// by the ssr webhook handler before ApplyWebhookEvent is reached.
const (
	outcomeWebhookProcessed         = "processed"
	outcomeWebhookIgnored           = "ignored"
	outcomeWebhookAmountMismatch    = "amount_mismatch"
	outcomeWebhookRefundMismatch    = "refund_mismatch"
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

	switch {
	case orderIsPaidOrLater(order.Status):
		// Replay: never downgrade (paid, shipped, delivered, or any
		// refund-family status — payment was captured in all of them, so a
		// late checkout.session.completed must never be misclassified as
		// paid_after_terminal), but re-attempt the cart cleanup in case an
		// earlier finalize crashed between the transition and the cart write.
		if err := s.clearCartAfterPayment(ctx, order); err != nil {
			return order, err
		}
		return order, nil
	case order.Status == commerce.OrderStatusPendingPayment:
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
		eventCheckoutSessionExpired,
		eventRefundCreated,
		eventRefundUpdated,
		eventRefundFailed:
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
				// Money moved for an order already in a terminal status: the
				// order can never reconcile, so keep the alarmed outcome (its
				// semantics are now "a refund was already issued unattended —
				// verify its legitimacy and settlement in Stripe") and issue
				// the refund automatically. The event is deliberately not
				// marked processed so a redelivery re-verifies the marker.
				s.recordStripeWebhook(outcomeWebhookPaidAfterTerminal)
				if refundErr := s.autoRefundTerminalPayment(ctx, order, event); refundErr != nil {
					// Transient: let Stripe redeliver checkout.session.completed
					// so the auto-refund is retried (FinalizePayment will land
					// here again).
					s.recordStripeWebhook(outcomeWebhookError)
					return refundErr
				}
				return nil
			}
			s.recordStripeWebhook(outcomeWebhookError)
			return err
		}
	case eventRefundCreated, eventRefundUpdated, eventRefundFailed:
		if err := s.applyRefundEvent(ctx, order, event); err != nil {
			if errors.Is(err, errRefundMismatch) {
				// Cross-check failed: the refund_mismatch metric was already
				// recorded inside applyRefundEvent. ACK without marking the
				// event processed — it can never reconcile (the
				// amount_mismatch precedent).
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

// RefundOrderAs issues a full provider refund for a paid, shipped, or
// delivered order (or retries one from refund_failed), recording the given
// status-history actor on the refund_pending transition. The provider refund
// is created before any order write, so a provider failure leaves the order
// untouched; the refund.* webhooks heal a crash between the two steps. A
// synchronously settled refund advances straight on to refunded (or
// refund_failed), with actor "stripe" on the settlement step. Reserved stock
// is released through the claim protocol only for orders that never shipped.
// Replays (double-click, repeated POST) converge without a second provider
// refund. Returns the freshest order alongside any error.
func (s *Service) RefundOrderAs(ctx context.Context, order commerce.Order, actor string) (commerce.Order, error) {
	if s.Payments == nil {
		s.recordCheckoutRefund(outcomeRefundProviderError)
		return order, ErrRefundProviderUnavailable
	}

	switch order.Status {
	case commerce.OrderStatusRefundPending, commerce.OrderStatusRefunded:
		// Replay: the refund is already issued (defensive only — the admin
		// handler 409s these statuses before the service is reached; the live
		// re-drivers for a dangling stock release are webhook redelivery and
		// the order-detail reconcile). Re-attempt an unreleased claim for
		// unshipped orders, then report success.
		if order.ShippedAt.IsZero() {
			if err := s.ClaimAndReleaseOrderStock(ctx, order.ID); err != nil {
				return order, err
			}
		}
		return order, nil
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered, commerce.OrderStatusRefundFailed:
	default:
		return order, fmt.Errorf("%w: order %q is %s", ErrOrderNotRefundable, order.ID, order.Status)
	}
	if order.StripePaymentIntentID == "" {
		return order, fmt.Errorf("%w: order %q", ErrRefundNotIssuable, order.ID)
	}

	// Provider refund first (idempotency-keyed per order+attempt), order
	// write second: a provider failure leaves the order exactly as it was,
	// and a crash between the two steps is healed by the refund.* webhooks.
	attempt := order.RefundAttempt + 1
	refund, err := s.Payments.CreateRefund(ctx, payments.RefundInput{
		OrderID:         order.ID,
		PaymentIntentID: order.StripePaymentIntentID,
		Attempt:         attempt,
	})
	if err != nil {
		s.recordCheckoutRefund(outcomeRefundProviderError)
		checkoutLogger.Error("checkout could not create the provider refund",
			slog.String("order_id", order.ID),
			slog.String("order_status", string(order.Status)),
			slog.Int("refund_attempt", attempt),
			slog.String("error", err.Error()),
		)
		return order, err
	}

	// RefundFailureReason pointer-to-empty clears the stale reason on a
	// refund_failed -> refund_pending retry; on first issuance it is a
	// harmless REMOVE of an absent attribute.
	clearReason := ""
	patch := commerce.OrderPatch{Actor: actor, StripeRefundID: &refund.ID, RefundAttempt: &attempt, RefundFailureReason: &clearReason}
	updated, err := s.Commerce.TransitionOrder(ctx, order.ID, order.Status, commerce.OrderStatusRefundPending, patch)
	switch {
	case err == nil:
		s.recordCheckoutRefund(outcomeRefundIssued)
	case errors.Is(err, commerce.ErrOrderTransitionConflict):
		// Adopt a concurrent winner already in the refund family (it wrote
		// the same refund ID via the shared idempotency key); anything else
		// (for example a concurrent advance to shipped) is reported — the
		// orphan refund converges on the next click or via its webhooks.
		current, found, getErr := s.Commerce.GetOrder(ctx, order.ID)
		if getErr != nil || !found || !orderIsRefundFamily(current.Status) {
			s.recordCheckoutRefund(outcomeRefundError)
			return order, err
		}
		updated = current
	default:
		s.recordCheckoutRefund(outcomeRefundError)
		return order, err
	}

	updated = s.applyRefundOutcome(ctx, updated, refund)

	if updated.ShippedAt.IsZero() {
		if err := s.ClaimAndReleaseOrderStock(ctx, order.ID); err != nil {
			// The refund itself is committed; ErrStockReleaseFailed propagates
			// so the caller can surface the stock banner and a retry re-drives
			// the release.
			return updated, err
		}
	}

	return updated, nil
}

// applyRefundOutcome moves a refund_pending order forward when the provider
// reports a terminal refund status. Settlements are recorded with actor
// "stripe" regardless of who initiated the refund. Idempotent; returns the
// freshest order it knows. Store failures are logged and recorded
// (outcome=error) but never unwind the issued refund.
func (s *Service) applyRefundOutcome(ctx context.Context, order commerce.Order, refund payments.Refund) commerce.Order {
	if order.StripeRefundID != "" && refund.ID != order.StripeRefundID {
		// By the time this runs, applyRefundEvent has already adopted any
		// legitimately newer attempt, so everything dropped here really is an
		// older or foreign refund.
		checkoutLogger.Warn("stale refund outcome ignored",
			slog.String("order_id", order.ID),
			slog.String("order_refund_id", order.StripeRefundID),
			slog.String("refund_id", refund.ID),
		)
		return order
	}

	switch refund.Status {
	case payments.RefundStatusSucceeded:
		if order.Status != commerce.OrderStatusRefundPending {
			return order
		}
		if refund.AmountCents > 0 && refund.AmountCents != order.TotalCents {
			// Warn, never block: a dashboard partial refund followed by our
			// full refund stays visible without stopping settlement.
			checkoutLogger.Warn("refund amount differs from order total",
				slog.String("order_id", order.ID),
				slog.String("refund_id", refund.ID),
				slog.Int("refund_amount_cents", refund.AmountCents),
				slog.Int("order_total_cents", order.TotalCents),
			)
		}
		refundedAt := s.now()
		updated, err := s.Commerce.TransitionOrder(ctx, order.ID, commerce.OrderStatusRefundPending, commerce.OrderStatusRefunded, commerce.OrderPatch{Actor: commerce.OrderActorStripe, RefundedAt: &refundedAt})
		if err != nil {
			return s.refundOutcomeTransitionFailed(ctx, order, refund, err)
		}
		s.recordCheckoutRefund(outcomeRefundSettled)
		return updated
	case payments.RefundStatusFailed:
		if order.Status != commerce.OrderStatusRefundPending {
			return order
		}
		reason := refund.FailureReason
		updated, err := s.Commerce.TransitionOrder(ctx, order.ID, commerce.OrderStatusRefundPending, commerce.OrderStatusRefundFailed, commerce.OrderPatch{Actor: commerce.OrderActorStripe, RefundFailureReason: &reason})
		if err != nil {
			return s.refundOutcomeTransitionFailed(ctx, order, refund, err)
		}
		s.recordCheckoutRefund(outcomeRefundFailed)
		checkoutLogger.Error("stripe reported the refund as failed",
			slog.String("order_id", order.ID),
			slog.String("refund_id", refund.ID),
			slog.String("failure_reason", refund.FailureReason),
		)
		return updated
	case payments.RefundStatusCanceled:
		// Customer-balance-only state; cannot occur for our card refunds.
		checkoutLogger.Warn("refund outcome canceled ignored",
			slog.String("order_id", order.ID),
			slog.String("refund_id", refund.ID),
		)
		return order
	}

	// pending / requires_action: still settling.
	return order
}

// refundOutcomeTransitionFailed classifies a failed settlement transition:
// losing the race to another settler is success (return the freshest order);
// anything else is logged and recorded — the refund.* webhooks re-drive it.
func (s *Service) refundOutcomeTransitionFailed(ctx context.Context, order commerce.Order, refund payments.Refund, err error) commerce.Order {
	if errors.Is(err, commerce.ErrOrderTransitionConflict) {
		current, found, getErr := s.Commerce.GetOrder(ctx, order.ID)
		if getErr == nil && found {
			if orderIsRefundFamily(current.Status) && current.Status != commerce.OrderStatusRefundPending {
				return current
			}
			order = current
		}
	}
	checkoutLogger.Error("checkout could not record the refund outcome",
		slog.String("order_id", order.ID),
		slog.String("refund_id", refund.ID),
		slog.String("refund_status", refund.Status),
		slog.String("error", err.Error()),
	)
	s.recordCheckoutRefund(outcomeRefundError)
	return order
}

// refundMatchesOrder authenticates a refund event against the order it claims
// (via metadata.order_id) to belong to: metadata routes, the payment intent
// authenticates (metadata is freely editable in the Stripe dashboard).
// Skipped when either side is empty: dashboard refunds retrieved without
// expansion never reach here (no order_id), and a paid order can legitimately
// lack a PI after an expandSessionCard failure.
func refundMatchesOrder(order commerce.Order, refund payments.Refund) bool {
	if order.StripePaymentIntentID == "" || refund.PaymentIntentID == "" {
		return true
	}
	return refund.PaymentIntentID == order.StripePaymentIntentID
}

// applyRefundEvent reconciles a provider refund event with the order. Forward-
// only and replay-safe: refund events for terminal never-paid orders settle or
// alarm the paid_after_terminal auto-refund without touching the status;
// paid/shipped/delivered orders are healed into refund_pending (the crash
// window between CreateRefund and the admin transition); refund_failed orders
// adopt a higher-attempt refund (the retry crash window) and drop
// lower-or-equal stale attempts; refund_pending orders take the refund
// outcome. Events whose payment intent does not match the order are rejected
// with errRefundMismatch (alarmed refund_mismatch outcome).
func (s *Service) applyRefundEvent(ctx context.Context, order commerce.Order, event payments.Event) error {
	refund := event.Refund
	if !refundMatchesOrder(order, refund) {
		checkoutLogger.Warn("refund webhook payment intent does not match the order",
			slog.String("event_id", event.ID),
			slog.String("event_type", event.Type),
			slog.String("order_id", order.ID),
			slog.String("order_payment_intent_id", order.StripePaymentIntentID),
			slog.String("refund_id", refund.ID),
			slog.String("refund_payment_intent_id", refund.PaymentIntentID),
		)
		s.recordStripeWebhook(outcomeWebhookRefundMismatch)
		return errRefundMismatch
	}

	switch order.Status {
	case commerce.OrderStatusCanceled, commerce.OrderStatusExpired, commerce.OrderStatusPaymentFailed:
		return s.applyTerminalOrderRefundEvent(ctx, order, event)
	case commerce.OrderStatusPendingPayment:
		checkoutLogger.Warn("refund webhook for a pending order ignored",
			slog.String("event_id", event.ID),
			slog.String("event_type", event.Type),
			slog.String("order_id", order.ID),
			slog.String("refund_id", refund.ID),
		)
		return nil
	case commerce.OrderStatusRefundFailed:
		switch {
		case refund.ID == order.StripeRefundID:
			// The current attempt's own event (e.g. a replayed refund.failed):
			// fall through to applyRefundOutcome, which no-ops on
			// refund_failed.
		case refund.Attempt > order.RefundAttempt:
			// Retry crash window: the admin's retry minted this newer refund
			// but crashed before TransitionOrder. Adopt it.
			clearReason := ""
			attempt := refund.Attempt
			patch := commerce.OrderPatch{Actor: commerce.OrderActorStripe, StripeRefundID: &refund.ID, RefundAttempt: &attempt, RefundFailureReason: &clearReason}
			updated, err := s.transitionAdoptingRefund(ctx, order, commerce.OrderStatusRefundFailed, patch)
			if err != nil {
				return err // transient: Stripe retries
			}
			s.recordCheckoutRefund(outcomeRefundIssued)
			order = updated
		default:
			// Older or unattributable (Attempt 0) refund: stale, drop.
			checkoutLogger.Warn("stale refund webhook for a superseded attempt ignored",
				slog.String("event_id", event.ID),
				slog.String("event_type", event.Type),
				slog.String("order_id", order.ID),
				slog.String("order_refund_id", order.StripeRefundID),
				slog.String("refund_id", refund.ID),
				slog.Int("order_refund_attempt", order.RefundAttempt),
				slog.Int("refund_attempt", refund.Attempt),
			)
			return nil
		}
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered:
		// Heal the issue-then-crash window: adopt the refund and enter
		// refund_pending with actor stripe.
		attempt := refund.Attempt // from metadata; our refunds always carry it
		if attempt == 0 {
			attempt = order.RefundAttempt + 1
		}
		patch := commerce.OrderPatch{Actor: commerce.OrderActorStripe, StripeRefundID: &refund.ID, RefundAttempt: &attempt}
		updated, err := s.transitionAdoptingRefund(ctx, order, order.Status, patch)
		if err != nil {
			return err // transient: Stripe retries
		}
		s.recordCheckoutRefund(outcomeRefundIssued)
		order = updated
	}

	// order is now refund-family.
	order = s.applyRefundOutcome(ctx, order, refund)
	if order.ShippedAt.IsZero() {
		// ErrStockReleaseFailed -> 500 -> Stripe redelivery re-drives it.
		return s.ClaimAndReleaseOrderStock(ctx, order.ID)
	}
	return nil
}

// applyTerminalOrderRefundEvent handles refund events for orders in a
// terminal never-paid status (the paid_after_terminal auto-refund): the order
// keeps its terminal status, but a failed auto-refund must not be silent —
// it is alarmed exactly like an admin refund failure and the reason is
// persisted for the admin order page (resolution path: Stripe dashboard;
// terminal orders get no in-app retry).
func (s *Service) applyTerminalOrderRefundEvent(ctx context.Context, order commerce.Order, event payments.Event) error {
	refund := event.Refund
	if order.StripeRefundID == "" || refund.ID != order.StripeRefundID {
		// Not our auto-refund: either the audit-marker patch never landed (a
		// checkout.session.completed redelivery re-runs it) or this is a
		// foreign refund. Never alarmed-failed here.
		checkoutLogger.Warn("refund webhook for a terminal order does not match its refund marker",
			slog.String("event_id", event.ID),
			slog.String("event_type", event.Type),
			slog.String("order_id", order.ID),
			slog.String("order_status", string(order.Status)),
			slog.String("order_refund_id", order.StripeRefundID),
			slog.String("refund_id", refund.ID),
		)
		return nil
	}
	if refund.Status == payments.RefundStatusFailed {
		if order.RefundFailureReason == refund.FailureReason && refund.FailureReason != "" {
			// Replay: the failure was already recorded and alarmed.
			return nil
		}
		checkoutLogger.Error("automatic refund for a terminal order failed; resolve in the Stripe dashboard",
			slog.String("order_id", order.ID),
			slog.String("order_status", string(order.Status)),
			slog.String("refund_id", refund.ID),
			slog.String("failure_reason", refund.FailureReason),
		)
		s.recordCheckoutRefund(outcomeRefundFailed)
		s.patchTerminalRefundFailureReason(ctx, order, refund.FailureReason)
		return nil
	}
	checkoutLogger.Info("refund webhook for a terminal order acknowledged",
		slog.String("event_id", event.ID),
		slog.String("event_type", event.Type),
		slog.String("order_id", order.ID),
		slog.String("order_status", string(order.Status)),
		slog.String("refund_id", refund.ID),
		slog.String("refund_status", refund.Status),
	)
	return nil
}

// patchTerminalRefundFailureReason best-effort persists the failure reason on
// a terminal order without changing its status: on a version conflict it
// re-reads and retries once, otherwise it warn-logs and gives up (the
// alarmed metric already fired; a webhook replay re-runs the patch).
func (s *Service) patchTerminalRefundFailureReason(ctx context.Context, order commerce.Order, reason string) {
	patch := commerce.OrderPatch{RefundFailureReason: &reason}
	_, err := s.Commerce.PatchOrder(ctx, order.ID, order.Status, order.Version, patch)
	if err == nil {
		return
	}
	if errors.Is(err, commerce.ErrVersionConflict) {
		current, found, getErr := s.Commerce.GetOrder(ctx, order.ID)
		if getErr == nil && found {
			if current.RefundFailureReason == reason {
				return
			}
			if _, retryErr := s.Commerce.PatchOrder(ctx, current.ID, current.Status, current.Version, patch); retryErr == nil {
				return
			}
		}
	}
	checkoutLogger.Warn("checkout could not persist the terminal-order refund failure reason",
		slog.String("order_id", order.ID),
		slog.String("error", err.Error()),
	)
}

// transitionAdoptingRefund wraps TransitionOrder(from -> refund_pending) with
// the shared conflict recovery: on ErrOrderTransitionConflict re-read and
// adopt a current refund-family status, otherwise propagate the error.
func (s *Service) transitionAdoptingRefund(ctx context.Context, order commerce.Order, from commerce.OrderStatus, patch commerce.OrderPatch) (commerce.Order, error) {
	updated, err := s.Commerce.TransitionOrder(ctx, order.ID, from, commerce.OrderStatusRefundPending, patch)
	if err != nil {
		if errors.Is(err, commerce.ErrOrderTransitionConflict) {
			current, found, getErr := s.Commerce.GetOrder(ctx, order.ID)
			if getErr == nil && found && orderIsRefundFamily(current.Status) {
				return current, nil
			}
		}
		return order, err
	}
	return updated, nil
}

// autoRefundTerminalPayment returns a payment captured for an order already
// in a terminal never-paid status (canceled/expired/payment_failed). The
// order's status is NOT changed — it was never fulfilled — but StripeRefundID
// is patched on as the audit marker and replay dedupe. Idempotent: a set
// marker short-circuits, and the provider idempotency key dedupes the rest.
// Returning an error means a transient failure Stripe should retry.
func (s *Service) autoRefundTerminalPayment(ctx context.Context, order commerce.Order, event payments.Event) error {
	if order.StripeRefundID != "" {
		checkoutLogger.Info("automatic refund already issued for the terminal order",
			slog.String("event_id", event.ID),
			slog.String("order_id", order.ID),
			slog.String("refund_id", order.StripeRefundID),
		)
		return nil
	}

	paymentIntentID := event.Session.PaymentIntentID
	if paymentIntentID == "" && s.Payments != nil {
		paymentIntentID = s.expandSessionCard(ctx, event.Session).PaymentIntentID
	}
	if paymentIntentID == "" || s.Payments == nil {
		// Retries cannot conjure a payment intent (and the webhook handler
		// 404s when payments is nil, so the nil guard is defensive): fall
		// back to the manual-refund log line and acknowledge.
		checkoutLogger.Error("stripe webhook delivered a paid session for a terminal order; refund manually",
			slog.String("event_id", event.ID),
			slog.String("event_type", event.Type),
			slog.String("order_id", order.ID),
			slog.String("order_status", string(order.Status)),
			slog.String("payment_intent_id", event.Session.PaymentIntentID),
		)
		s.recordCheckoutRefund(outcomeRefundProviderError)
		return nil
	}

	attempt := order.RefundAttempt + 1
	refund, err := s.Payments.CreateRefund(ctx, payments.RefundInput{
		OrderID:         order.ID,
		PaymentIntentID: paymentIntentID,
		Attempt:         attempt,
	})
	if err != nil {
		s.recordCheckoutRefund(outcomeRefundProviderError)
		checkoutLogger.Error("checkout could not create the automatic refund for a terminal order",
			slog.String("event_id", event.ID),
			slog.String("order_id", order.ID),
			slog.String("order_status", string(order.Status)),
			slog.String("payment_intent_id", paymentIntentID),
			slog.String("error", err.Error()),
		)
		return err
	}

	// Audit marker (status unchanged). On a version conflict, a set marker
	// means another delivery won; otherwise warn and continue — the refund
	// exists at Stripe and a redelivery re-runs this patch.
	if _, err := s.Commerce.PatchOrder(ctx, order.ID, order.Status, order.Version, commerce.OrderPatch{StripeRefundID: &refund.ID, RefundAttempt: &attempt}); err != nil {
		markerSet := false
		if errors.Is(err, commerce.ErrVersionConflict) {
			current, found, getErr := s.Commerce.GetOrder(ctx, order.ID)
			markerSet = getErr == nil && found && current.StripeRefundID != ""
		}
		if !markerSet {
			checkoutLogger.Warn("checkout could not record the automatic refund marker",
				slog.String("order_id", order.ID),
				slog.String("refund_id", refund.ID),
				slog.String("error", err.Error()),
			)
		}
	}

	s.recordCheckoutRefund(outcomeRefundIssuedAuto)
	// The runbook anchor: when the paid_after_terminal alarm fires, ops
	// verifies the refund's legitimacy and settlement in Stripe.
	checkoutLogger.Error("payment captured for a terminal order; automatic refund issued",
		slog.String("event_id", event.ID),
		slog.String("event_type", event.Type),
		slog.String("order_id", order.ID),
		slog.String("order_status", string(order.Status)),
		slog.String("payment_intent_id", paymentIntentID),
		slog.String("refund_id", refund.ID),
	)
	return nil
}

// ReconcileRefund re-checks a refund-family order against the provider and
// re-drives anything left dangling — the order-detail render backstop for
// missed or unregistered refund webhooks AND for a stock release that failed
// after the refund's webhook events were already acknowledged. Best-effort
// and idempotent: every failure degrades to returning the order unchanged.
func (s *Service) ReconcileRefund(ctx context.Context, order commerce.Order) commerce.Order {
	if !orderIsRefundFamily(order.Status) {
		return order
	}

	if order.Status == commerce.OrderStatusRefundPending && order.StripeRefundID != "" && s.Payments != nil {
		refund, err := s.Payments.GetRefund(ctx, order.StripeRefundID)
		if err != nil {
			checkoutLogger.Warn("checkout could not reconcile the pending refund",
				slog.String("order_id", order.ID),
				slog.String("refund_id", order.StripeRefundID),
				slog.String("error", err.Error()),
			)
		} else {
			order = s.applyRefundOutcome(ctx, order, refund)
		}
	}

	// Stock re-drive: the StockReleasedAt pre-check on the already-loaded
	// order makes the common case free; ClaimAndReleaseOrderStock itself
	// early-returns on a set marker, so replays cost one read.
	if order.ShippedAt.IsZero() && order.StockReleasedAt.IsZero() {
		if err := s.ClaimAndReleaseOrderStock(ctx, order.ID); err != nil {
			checkoutLogger.Error("checkout could not re-drive the refund stock release",
				slog.String("order_id", order.ID),
				slog.String("error", err.Error()),
			)
		}
	}

	return order
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
		if !orderStockReleasable(order) {
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

func (s *Service) recordCheckoutRefund(outcome string) {
	if s.Metrics == nil {
		return
	}
	s.Metrics.Record(observability.Count(
		observability.MetricCheckoutRefund,
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

// orderIsPaidOrLater reports whether the order's payment was captured: the
// fulfillment chain and the refund family (a refunded order was paid first).
func orderIsPaidOrLater(status commerce.OrderStatus) bool {
	switch status {
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered,
		commerce.OrderStatusRefundPending, commerce.OrderStatusRefunded, commerce.OrderStatusRefundFailed:
		return true
	}
	return false
}

// orderIsRefundFamily reports whether the order is in a refund lifecycle
// status (refund_pending, refunded, or refund_failed).
func orderIsRefundFamily(status commerce.OrderStatus) bool {
	switch status {
	case commerce.OrderStatusRefundPending, commerce.OrderStatusRefunded, commerce.OrderStatusRefundFailed:
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

// orderStockReleasable reports whether the order's reservation may be
// returned to the catalog: any stock-releasing terminal status, or a
// refund-family status on an order that never shipped (goods still here).
func orderStockReleasable(order commerce.Order) bool {
	if orderStockReleased(order.Status) {
		return true
	}
	if orderIsRefundFamily(order.Status) {
		return order.ShippedAt.IsZero()
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
