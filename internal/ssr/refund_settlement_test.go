package ssr

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
)

type unavailableRefundStore struct {
	commerce.Store
	fail bool
}

func (s *unavailableRefundStore) TransitionOrder(ctx context.Context, id string, from, to commerce.OrderStatus, patch commerce.OrderPatch) (commerce.Order, error) {
	if s.fail && from == commerce.OrderStatusRefundPending {
		return commerce.Order{}, errors.New("settlement unavailable")
	}
	return s.Store.TransitionOrder(ctx, id, from, to, patch)
}

func TestWebhookSettlementFailureReturnsRetryableResponse(t *testing.T) {
	env := newAccountTestEnv(t)
	jar, addressID := checkoutTestSetup(t, env, 2)
	orderID, sessionID := placeTestOrder(t, env, jar, addressID)
	refundPendingWebhookOrder(t, env, orderID, sessionID)
	store := &unavailableRefundStore{Store: env.commerce, fail: true}
	env.handler.checkout.Commerce = store
	payload := refundEventPayload("evt_settlement_retry", "refund.updated", "re_test_"+orderID, orderID, 1, "pi_test_webhook", "succeeded", "", 5798)
	request := fakeSignedWebhookRequest(payload, time.Now())
	response, err := env.handler.Handle(context.Background(), request)
	if err != nil || response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failed settlement = %d %v", response.StatusCode, err)
	}
	if got := mangoStock(t, env); got != 3 {
		t.Fatalf("stock released before settlement: %d", got)
	}
	store.fail = false
	for range 2 {
		response, err = env.handler.Handle(context.Background(), request)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("replayed settlement = %d %v", response.StatusCode, err)
		}
	}
	if got := mangoStock(t, env); got != 5 {
		t.Fatalf("replay released stock more than once: %d", got)
	}
}
