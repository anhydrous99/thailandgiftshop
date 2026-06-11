package commerce

import (
	"testing"
	"time"
)

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{name: "already normalized", email: "shopper@example.test", want: "shopper@example.test"},
		{name: "mixed case", email: "Shopper@Example.TEST", want: "shopper@example.test"},
		{name: "surrounding whitespace", email: "  shopper@example.test \t", want: "shopper@example.test"},
		{name: "empty", email: "", want: ""},
		{name: "whitespace only", email: "   ", want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeEmail(test.email); got != test.want {
				t.Fatalf("NormalizeEmail(%q) = %q, want %q", test.email, got, test.want)
			}
		})
	}
}

func TestValidOrderStatus(t *testing.T) {
	for _, status := range []OrderStatus{
		OrderStatusPendingPayment, OrderStatusPaid, OrderStatusShipped,
		OrderStatusDelivered, OrderStatusPaymentFailed, OrderStatusExpired, OrderStatusCanceled,
		OrderStatusRefundPending, OrderStatusRefunded, OrderStatusRefundFailed,
	} {
		if !ValidOrderStatus(status) {
			t.Fatalf("ValidOrderStatus(%s) = false, want true", status)
		}
	}
	for _, status := range []OrderStatus{"", "PAID", "pending"} {
		if ValidOrderStatus(status) {
			t.Fatalf("ValidOrderStatus(%q) = true, want false", status)
		}
	}
}

func TestAllowedOrderTransitionRejectsUnknownStatuses(t *testing.T) {
	if AllowedOrderTransition("returned", OrderStatusPaid) {
		t.Fatalf("unknown from-status allowed")
	}
	if AllowedOrderTransition(OrderStatusPaid, "returned") {
		t.Fatalf("unknown to-status allowed")
	}
	if AllowedOrderTransition(OrderStatusPaid, OrderStatusPaid) {
		t.Fatalf("self transition allowed")
	}
}

func TestOrderTransitionsRefundFamily(t *testing.T) {
	allowed := []struct{ from, to OrderStatus }{
		{OrderStatusPaid, OrderStatusRefundPending},
		{OrderStatusShipped, OrderStatusRefundPending},
		{OrderStatusDelivered, OrderStatusRefundPending},
		{OrderStatusRefundPending, OrderStatusRefunded},
		{OrderStatusRefundPending, OrderStatusRefundFailed},
		{OrderStatusRefundFailed, OrderStatusRefundPending},
	}
	for _, transition := range allowed {
		if !AllowedOrderTransition(transition.from, transition.to) {
			t.Fatalf("AllowedOrderTransition(%s, %s) = false, want true", transition.from, transition.to)
		}
	}

	// paid -> canceled is deliberately removed: a paid order can only leave
	// the paid family through a refund.
	if AllowedOrderTransition(OrderStatusPaid, OrderStatusCanceled) {
		t.Fatalf("AllowedOrderTransition(paid, canceled) = true, want false")
	}
	// refunded must always pass through refund_pending.
	if AllowedOrderTransition(OrderStatusPaid, OrderStatusRefunded) {
		t.Fatalf("AllowedOrderTransition(paid, refunded) = true, want false")
	}
	// refunded is terminal.
	for _, to := range []OrderStatus{
		OrderStatusPendingPayment, OrderStatusPaid, OrderStatusShipped, OrderStatusDelivered,
		OrderStatusPaymentFailed, OrderStatusExpired, OrderStatusCanceled,
		OrderStatusRefundPending, OrderStatusRefundFailed,
	} {
		if AllowedOrderTransition(OrderStatusRefunded, to) {
			t.Fatalf("AllowedOrderTransition(refunded, %s) = true, want false", to)
		}
	}
}

func TestOrderActorOrDefault(t *testing.T) {
	if got := orderActorOrDefault(""); got != OrderActorSystem {
		t.Fatalf("orderActorOrDefault(\"\") = %q, want %q", got, OrderActorSystem)
	}
	if got := orderActorOrDefault("  "); got != OrderActorSystem {
		t.Fatalf("orderActorOrDefault(blank) = %q, want %q", got, OrderActorSystem)
	}
	if got := orderActorOrDefault(OrderActorAdmin); got != OrderActorAdmin {
		t.Fatalf("orderActorOrDefault(admin) = %q, want admin", got)
	}
}

func TestApplyOrderPatchFields(t *testing.T) {
	paidAt := time.Date(2026, 6, 8, 12, 30, 0, 0, time.UTC)
	releasedAt := time.Date(2026, 6, 8, 12, 45, 0, 0, time.UTC)
	order := Order{
		Status:                  OrderStatusPendingPayment,
		StripeCheckoutSessionID: "cs_old",
		TrackingNumber:          "old-number",
	}

	applyOrderPatchFields(&order, OrderPatch{
		StripeCheckoutSessionID: ptr(""),
		StripePaymentIntentID:   ptr("pi_1"),
		PaymentCardBrand:        ptr("visa"),
		PaymentCardLast4:        ptr("4242"),
		CheckoutAttempt:         ptr(2),
		PaidAt:                  &paidAt,
		StockReleasedAt:         &releasedAt,
	})

	if order.StripeCheckoutSessionID != "" {
		t.Fatalf("empty-string pointer did not clear the field: %q", order.StripeCheckoutSessionID)
	}
	if order.StripePaymentIntentID != "pi_1" || order.PaymentCardBrand != "visa" || order.PaymentCardLast4 != "4242" {
		t.Fatalf("patched order = %#v", order)
	}
	if order.CheckoutAttempt != 2 || !order.PaidAt.Equal(paidAt) {
		t.Fatalf("patched order = %#v", order)
	}
	if !order.StockReleasedAt.Equal(releasedAt) {
		t.Fatalf("StockReleasedAt = %s, want %s", order.StockReleasedAt, releasedAt)
	}
	if order.TrackingNumber != "old-number" {
		t.Fatalf("nil pointer overwrote untouched field: %q", order.TrackingNumber)
	}

	// A pointer to the zero time clears the marker.
	applyOrderPatchFields(&order, OrderPatch{StockReleasedAt: &time.Time{}})
	if !order.StockReleasedAt.IsZero() {
		t.Fatalf("StockReleasedAt = %s after clear, want zero", order.StockReleasedAt)
	}
}
