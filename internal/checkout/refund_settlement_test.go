package checkout

import (
	"context"
	"errors"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
)

type failingRefundSettlementStore struct {
	commerce.Store
	fail           bool
	processedCalls int
}

func (s *failingRefundSettlementStore) MarkStripeEventProcessed(ctx context.Context, eventID, eventType, orderID string) (bool, error) {
	s.processedCalls++
	return s.Store.MarkStripeEventProcessed(ctx, eventID, eventType, orderID)
}

var errSettlementUnavailable = errors.New("settlement store unavailable")

func (s *failingRefundSettlementStore) TransitionOrder(ctx context.Context, id string, from, to commerce.OrderStatus, patch commerce.OrderPatch) (commerce.Order, error) {
	if s.fail && from == commerce.OrderStatusRefundPending {
		return commerce.Order{}, errSettlementUnavailable
	}
	return s.Store.TransitionOrder(ctx, id, from, to, patch)
}

func TestRefundWebhookRetriesSettlementPersistenceFailure(t *testing.T) {
	for _, status := range []string{payments.RefundStatusSucceeded, payments.RefundStatusFailed} {
		t.Run(status, func(t *testing.T) {
			env := newTestEnv(t)
			order := env.mustPaidOrder(t)
			env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
			if _, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin); err != nil {
				t.Fatal(err)
			}
			store := &failingRefundSettlementStore{Store: env.service.Commerce, fail: true}
			env.service.Commerce = store
			event := refundWebhookEvent("evt_settlement_retry", "refund.updated", payments.Refund{
				ID: "re_fake_" + order.ID + "_1", PaymentIntentID: "pi_fake_" + order.ID, OrderID: order.ID, Attempt: 1, Status: status, AmountCents: order.TotalCents,
			})
			if err := env.service.ApplyWebhookEvent(context.Background(), event); !errors.Is(err, errSettlementUnavailable) {
				t.Fatalf("webhook error = %v", err)
			}
			if current := env.mustGetOrder(t, order.ID); current.Status != commerce.OrderStatusRefundPending {
				t.Fatal("failed write changed order")
			}
			if store.processedCalls != 0 {
				t.Fatal("failed settlement marked event processed")
			}
			store.fail = false
			if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			expected := commerce.OrderStatusRefunded
			metric := "settled"
			if status == payments.RefundStatusFailed {
				expected = commerce.OrderStatusRefundFailed
				metric = "failed"
			}
			if current := env.mustGetOrder(t, order.ID); current.Status != expected {
				t.Fatalf("replay status = %s", current.Status)
			}
			if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if env.provider.createRefundCalls != 1 {
				t.Fatal("replay issued another refund")
			}
			if got := env.metrics.count(observability.MetricCheckoutRefund, metric); got != 1 {
				t.Fatalf("settlement count = %d", got)
			}
			env.assertStock(t, 5, 4)
		})
	}
}

func TestIssuedRefundAndAdminReconciliationRemainBestEffort(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	store := &failingRefundSettlementStore{Store: env.service.Commerce, fail: true}
	env.service.Commerce = store
	pending, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if err != nil || pending.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("issued refund = %s %v", pending.Status, err)
	}
	if current := env.service.ReconcileRefund(context.Background(), pending); current.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("failed reconciliation = %s", current.Status)
	}
	store.fail = false
	settled := env.service.ReconcileRefund(context.Background(), pending)
	if settled.Status != commerce.OrderStatusRefunded {
		t.Fatalf("reconciliation replay = %s", settled.Status)
	}
	if env.provider.createRefundCalls != 1 {
		t.Fatal("reconciliation reissued refund")
	}
	env.assertStock(t, 5, 4)
}

func TestRefundSettlementConflictRequiresEquivalentOutcome(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
	pending, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.Commerce.TransitionOrder(context.Background(), order.ID, commerce.OrderStatusRefundPending, commerce.OrderStatusRefunded, commerce.OrderPatch{}); err != nil {
		t.Fatal(err)
	}
	refund := payments.Refund{ID: pending.StripeRefundID, Status: payments.RefundStatusSucceeded}
	if _, err := env.service.refundOutcomeTransitionFailed(context.Background(), pending, refund, commerce.ErrOrderTransitionConflict); err != nil {
		t.Fatalf("equivalent winner: %v", err)
	}
	refund.Status = payments.RefundStatusFailed
	if _, err := env.service.refundOutcomeTransitionFailed(context.Background(), pending, refund, commerce.ErrOrderTransitionConflict); err == nil {
		t.Fatal("different outcome treated as success")
	}
	refund.Status = payments.RefundStatusSucceeded
	refund.ID = "different-attempt"
	if _, err := env.service.refundOutcomeTransitionFailed(context.Background(), pending, refund, commerce.ErrOrderTransitionConflict); err == nil {
		t.Fatal("different refund treated as success")
	}
}
