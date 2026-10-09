package checkout

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
)

type refundPauseKey struct{}
type refundPausedStore struct {
	commerce.Store
	entered   chan struct{}
	resume    chan struct{}
	from      commerce.OrderStatus
	claims    int
	markerErr error
	getErr    error
	missing   bool
	writeErr  error
}

func (s *refundPausedStore) TransitionOrder(ctx context.Context, id string, from, to commerce.OrderStatus, patch commerce.OrderPatch) (commerce.Order, error) {
	if ctx.Value(refundPauseKey{}) == true && from == s.from {
		select {
		case s.entered <- struct{}{}:
		case <-ctx.Done():
			return commerce.Order{}, ctx.Err()
		}
		select {
		case <-s.resume:
		case <-ctx.Done():
			return commerce.Order{}, ctx.Err()
		}
	}
	if s.writeErr != nil {
		return commerce.Order{}, s.writeErr
	}
	return s.Store.TransitionOrder(ctx, id, from, to, patch)
}
func (s *refundPausedStore) PatchOrder(ctx context.Context, id string, status commerce.OrderStatus, version int, patch commerce.OrderPatch) (commerce.Order, error) {
	if patch.StockReleasedAt != nil {
		s.claims++
	}
	return s.Store.PatchOrder(ctx, id, status, version, patch)
}
func (s *refundPausedStore) MarkStripeEventProcessed(ctx context.Context, id, kind, order string) (bool, error) {
	if s.markerErr != nil {
		return false, s.markerErr
	}
	return s.Store.MarkStripeEventProcessed(ctx, id, kind, order)
}
func (s *refundPausedStore) GetOrder(ctx context.Context, id string) (commerce.Order, bool, error) {
	if s.getErr != nil {
		return commerce.Order{}, false, s.getErr
	}
	if s.missing {
		return commerce.Order{}, false, nil
	}
	return s.Store.GetOrder(ctx, id)
}

// No sleep schedules. The losing delivery is tagged so the real mutation
// intercept pauses only it, while all legacy recorders are used serially.
// Cleanup cancels and joins a worker on every assertion failure path.
func startRefundLoser(t *testing.T, store *refundPausedStore, fn func(context.Context) error) func() error {
	t.Helper()
	// Only the test goroutine owns the watchdog. A worker deadline must not
	// independently release the paused loser into unsynchronized recorders
	// while the winner is active. Cleanup cancels after test actions stop.
	ctx, cancel := context.WithCancel(context.Background())
	watchdog, stopWatchdog := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(stopWatchdog)
	done := make(chan error, 1)
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Error("refund worker did not exit")
			}
		}
	})
	go func() { done <- fn(context.WithValue(ctx, refundPauseKey{}, true)) }()
	select {
	case <-store.entered:
	case <-watchdog.Done():
		t.Fatal("actual TransitionOrder intercept not reached:", watchdog.Err())
	}
	return func() error {
		close(store.resume)
		select {
		case err := <-done:
			joined = true
			return err
		case <-watchdog.Done():
			t.Fatal(watchdog.Err())
			return watchdog.Err()
		}
	}
}
func refundRaceStore(env *testEnv, from commerce.OrderStatus) *refundPausedStore {
	store := &refundPausedStore{Store: env.service.Commerce, from: from, entered: make(chan struct{}, 1), resume: make(chan struct{})}
	env.service.Commerce = store
	return store
}
func refundRacePending(t *testing.T, env *testEnv) commerce.Order {
	t.Helper()
	paid := env.mustPaidOrder(t)
	id, attempt := "re_1", 1
	pending, err := env.commerceStore.TransitionOrder(context.Background(), paid.ID, paid.Status, commerce.OrderStatusRefundPending, commerce.OrderPatch{StripeRefundID: &id, RefundAttempt: &attempt})
	if err != nil {
		t.Fatal(err)
	}
	return pending // intentionally unclaimed stock
}
func refundRaceEvent(order commerce.Order, id, status, reason string) payments.Event {
	return refundWebhookEvent(id, "refund.updated", payments.Refund{ID: order.StripeRefundID, OrderID: order.ID, PaymentIntentID: order.StripePaymentIntentID, Attempt: order.RefundAttempt, Status: status, FailureReason: reason, AmountCents: order.TotalCents})
}

type refundEffects struct{ failed, settled, issued, emails, markers, creates, stock, claims int }

func refundRaceEffects(env *testEnv, store *refundPausedStore, stock *flakyStockStore) refundEffects {
	return refundEffects{env.metrics.count(observability.MetricCheckoutRefund, "failed"), env.metrics.count(observability.MetricCheckoutRefund, "settled"), env.metrics.count(observability.MetricCheckoutRefund, "issued"), len(env.emailSender.Messages()), env.hooks.markStripeEventCalls, env.provider.createRefundCalls, stock.calls, store.claims}
}
func refundRaceUnchanged(t *testing.T, env *testEnv, before commerce.Order) {
	t.Helper()
	after := env.mustGetOrder(t, before.ID)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("order changed: before=%+v after=%+v", before, after)
	}
}

func TestRefundPausedDeliveryCannotSettleSupersededAttempt(t *testing.T) {
	for _, tc := range []struct{ name, old, new, reason string }{
		{"failed_into_pending", payments.RefundStatusFailed, payments.RefundStatusPending, "old_failure"},
		{"succeeded_into_pending", payments.RefundStatusSucceeded, payments.RefundStatusPending, ""},
		{"same_status_same_reason", payments.RefundStatusFailed, payments.RefundStatusFailed, "old_failure"},
		{"same_status_different_reason", payments.RefundStatusFailed, payments.RefundStatusFailed, "new_failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			ctx := context.Background()
			paid := env.mustPaidOrder(t)
			env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
			pending, err := env.service.RefundOrderAs(ctx, paid, commerce.OrderActorAdmin)
			if err != nil {
				t.Fatal(err)
			}
			stock := &flakyStockStore{StockStore: env.service.Stock}
			env.service.Stock = stock
			store := refundRaceStore(env, commerce.OrderStatusRefundPending)
			stale := refundRaceEvent(pending, "evt_loser", tc.old, "old_failure")
			finish := startRefundLoser(t, store, func(ctx context.Context) error { return env.service.ApplyWebhookEvent(ctx, stale) })
			if err := env.service.ApplyWebhookEvent(ctx, refundRaceEvent(pending, "evt_winner", payments.RefundStatusFailed, "winner_failure")); err != nil {
				t.Fatal(err)
			}
			if err := env.provider.SettleRefund(pending.StripeRefundID, payments.RefundStatusFailed, "winner_failure"); err != nil {
				t.Fatal(err)
			}
			env.provider.SetNextRefundOutcome(tc.new, tc.reason)
			if _, err := env.service.RefundOrderAs(ctx, env.mustGetOrder(t, paid.ID), commerce.OrderActorAdmin); err != nil {
				t.Fatal(err)
			}
			before := env.mustGetOrder(t, paid.ID)
			effects := refundRaceEffects(env, store, stock)
			if before.RefundAttempt != 2 || before.StripeRefundID == pending.StripeRefundID {
				t.Fatal("attempt 2 not established")
			}
			if err := finish(); !errors.Is(err, commerce.ErrOrderTransitionConflict) {
				t.Errorf("loser error=%v, want unresolved conflict", err)
			}
			refundRaceUnchanged(t, env, before)
			if got := refundRaceEffects(env, store, stock); got != effects {
				t.Errorf("stale lifecycle/marker effects: before=%+v after=%+v", effects, got)
			}
			env.assertStock(t, 5, 4)
			// The same delivery may be ACKed only after fresh verification proves
			// it stale. That audit marker is safe; no terminal/stock effects occur.
			if err := env.service.ApplyWebhookEvent(ctx, stale); err != nil {
				t.Fatal(err)
			}
			effects.markers++
			if got := refundRaceEffects(env, store, stock); got != effects {
				t.Errorf("fresh stale replay effects: %+v", got)
			}
			refundRaceUnchanged(t, env, before)
		})
	}
}

func TestRefundEquivalentConcurrentDeliveries(t *testing.T) {
	for _, status := range []string{payments.RefundStatusSucceeded, payments.RefundStatusFailed} {
		for _, sameEvent := range []bool{false, true} {
			t.Run(status+map[bool]string{false: "/different_event", true: "/same_event"}[sameEvent], func(t *testing.T) {
				env := newTestEnv(t)
				pending := refundRacePending(t, env)
				stock := &flakyStockStore{StockStore: env.service.Stock}
				env.service.Stock = stock
				store := refundRaceStore(env, commerce.OrderStatusRefundPending)
				loserID := "evt_loser"
				if sameEvent {
					loserID = "evt_winner"
				}
				loser := refundRaceEvent(pending, loserID, status, "same_failure")
				finish := startRefundLoser(t, store, func(ctx context.Context) error { return env.service.ApplyWebhookEvent(ctx, loser) })
				if err := env.service.ApplyWebhookEvent(context.Background(), refundRaceEvent(pending, "evt_winner", status, "same_failure")); err != nil {
					t.Fatal(err)
				}
				before := env.mustGetOrder(t, pending.ID)
				effects := refundRaceEffects(env, store, stock)
				if stock.calls != 1 || store.claims != 1 || len(before.StatusHistory) != len(pending.StatusHistory)+1 {
					t.Fatalf("winner effects=%+v history=%d", effects, len(before.StatusHistory))
				}
				// Winner's stock claim increments version after its terminal email.
				// The loser must not create a new notification for that new version.
				if err := finish(); err != nil {
					t.Fatal(err)
				}
				effects.markers++
				if got := refundRaceEffects(env, store, stock); got != effects {
					t.Errorf("equivalent loser repeated effects: before=%+v after=%+v", effects, got)
				}
				refundRaceUnchanged(t, env, before)
				env.assertStock(t, 5, 4)
				if effects.failed+effects.settled != 1 {
					t.Fatal("terminal metric must occur once")
				}
			})
		}
	}
}

func TestRefundPendingVersionConflictAndNonEquivalentOutcomes(t *testing.T) {
	for _, outcome := range []string{"pending_patch", "different_reason", "different_status", "reread_error", "missing", "raw_write_error", "same_id_different_attempt"} {
		t.Run(outcome, func(t *testing.T) {
			env := newTestEnv(t)
			pending := refundRacePending(t, env)
			stock := &flakyStockStore{StockStore: env.service.Stock}
			env.service.Stock = stock
			store := refundRaceStore(env, commerce.OrderStatusRefundPending)
			event := refundRaceEvent(pending, "evt_loser", payments.RefundStatusFailed, "old_reason")
			finish := startRefundLoser(t, store, func(ctx context.Context) error { return env.service.ApplyWebhookEvent(ctx, event) })
			injected := errors.New("read or write unavailable")
			switch outcome {
			case "same_id_different_attempt":
				attempt, reason := pending.RefundAttempt+1, "old_reason"
				if _, err := env.commerceStore.TransitionOrder(context.Background(), pending.ID, pending.Status, commerce.OrderStatusRefundFailed, commerce.OrderPatch{RefundAttempt: &attempt, RefundFailureReason: &reason}); err != nil {
					t.Fatal(err)
				}
			case "pending_patch", "reread_error", "missing":
				if _, err := env.commerceStore.PatchOrder(context.Background(), pending.ID, pending.Status, pending.Version, commerce.OrderPatch{}); err != nil {
					t.Fatal(err)
				}
			case "different_reason", "different_status", "raw_write_error":
				target := commerce.OrderStatusRefundFailed
				reason := "different_reason"
				if outcome == "different_status" {
					target = commerce.OrderStatusRefunded
				}
				if outcome == "raw_write_error" {
					reason = "old_reason"
				}
				if _, err := env.commerceStore.TransitionOrder(context.Background(), pending.ID, pending.Status, target, commerce.OrderPatch{RefundFailureReason: &reason}); err != nil {
					t.Fatal(err)
				}
			}
			before := env.mustGetOrder(t, pending.ID)
			effects := refundRaceEffects(env, store, stock)
			if outcome == "reread_error" {
				store.getErr = injected
			}
			if outcome == "missing" {
				store.missing = true
			}
			if outcome == "raw_write_error" {
				store.writeErr = injected
			}
			err := finish()
			if outcome == "raw_write_error" {
				if !errors.Is(err, injected) || errors.Is(err, commerce.ErrOrderTransitionConflict) {
					t.Errorf("raw write masked: %v", err)
				}
			} else if !errors.Is(err, commerce.ErrOrderTransitionConflict) {
				t.Errorf("non-equivalent loser=%v", err)
			}
			if outcome == "reread_error" && !errors.Is(err, injected) {
				t.Errorf("read failure lost: %v", err)
			}
			store.getErr = nil
			store.missing = false
			store.writeErr = nil
			refundRaceUnchanged(t, env, before)
			if got := refundRaceEffects(env, store, stock); got != effects {
				t.Errorf("failed settlement effects=%+v want=%+v", got, effects)
			}
			if outcome == "pending_patch" {
				if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
					t.Fatal(err)
				}
				if env.mustGetOrder(t, pending.ID).Status != commerce.OrderStatusRefundFailed || env.provider.createRefundCalls != 0 {
					t.Fatal("fresh retry must settle without another provider refund")
				}
			}
		})
	}
}

func TestRefundFreshStaleReplayDoesNotClaimUnreleasedStock(t *testing.T) {
	for _, status := range []commerce.OrderStatus{commerce.OrderStatusRefundPending, commerce.OrderStatusRefunded, commerce.OrderStatusRefundFailed} {
		t.Run(string(status), func(t *testing.T) {
			env := newTestEnv(t)
			pending := refundRacePending(t, env)
			id, attempt := "re_2", 2
			current, err := env.commerceStore.PatchOrder(context.Background(), pending.ID, pending.Status, pending.Version, commerce.OrderPatch{StripeRefundID: &id, RefundAttempt: &attempt})
			if err != nil {
				t.Fatal(err)
			}
			if status != current.Status {
				current, err = env.commerceStore.TransitionOrder(context.Background(), current.ID, current.Status, status, commerce.OrderPatch{})
				if err != nil {
					t.Fatal(err)
				}
			}
			stock := &flakyStockStore{StockStore: env.service.Stock}
			env.service.Stock = stock
			store := refundRaceStore(env, commerce.OrderStatusRefundPending)
			effects := refundRaceEffects(env, store, stock)
			if err := env.service.ApplyWebhookEvent(context.Background(), refundRaceEvent(pending, "evt_stale", payments.RefundStatusSucceeded, "")); err != nil {
				t.Fatal(err)
			}
			effects.markers++
			if got := refundRaceEffects(env, store, stock); got != effects {
				t.Errorf("stale replay claimed stock or notified: %+v want %+v", got, effects)
			}
			refundRaceUnchanged(t, env, current)
			env.assertStock(t, 3, 3)
		})
	}
}

func TestRefundCommittedSettlementRetryRepairsOnlyRemainingWork(t *testing.T) {
	for _, failure := range []string{"stock", "marker", "shipped"} {
		t.Run(failure, func(t *testing.T) {
			env := newTestEnv(t)
			pending := refundRacePending(t, env)
			if failure == "shipped" {
				at := checkoutTestNow
				var err error
				pending, err = env.commerceStore.PatchOrder(context.Background(), pending.ID, pending.Status, pending.Version, commerce.OrderPatch{ShippedAt: &at})
				if err != nil {
					t.Fatal(err)
				}
			}
			stock := &flakyStockStore{StockStore: env.service.Stock}
			env.service.Stock = stock
			store := refundRaceStore(env, commerce.OrderStatusRefundPending)
			if failure == "stock" {
				stock.failures = 1
			}
			injected := errors.New("marker unavailable")
			if failure == "marker" {
				store.markerErr = injected
			}
			event := refundRaceEvent(pending, "evt_retry", payments.RefundStatusSucceeded, "")
			err := env.service.ApplyWebhookEvent(context.Background(), event)
			if failure == "stock" && !errors.Is(err, ErrStockReleaseFailed) {
				t.Fatal(err)
			}
			if failure == "marker" && !errors.Is(err, injected) {
				t.Fatal(err)
			}
			if failure == "shipped" && err != nil {
				t.Fatal(err)
			}
			committed := env.mustGetOrder(t, pending.ID)
			if committed.Status != commerce.OrderStatusRefunded || len(committed.StatusHistory) != len(pending.StatusHistory)+1 {
				t.Fatal("settlement not committed")
			}
			if failure != "shipped" && env.hooks.markStripeEventCalls != 0 {
				t.Fatal("failed work marked processed")
			}
			emails := len(env.emailSender.Messages())
			store.markerErr = nil
			for i := 0; i < 2; i++ {
				if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
					t.Fatal(err)
				}
			}
			after := env.mustGetOrder(t, pending.ID)
			if len(after.StatusHistory) != len(committed.StatusHistory) || !after.RefundedAt.Equal(committed.RefundedAt) || len(env.emailSender.Messages()) != emails || env.metrics.count(observability.MetricCheckoutRefund, "settled") != 1 || env.provider.createRefundCalls != 0 {
				t.Fatal("replay repeated settlement effects")
			}
			wantCalls := 1
			if failure == "stock" {
				wantCalls = 2
			}
			if failure == "shipped" {
				wantCalls = 0
			}
			if stock.calls != wantCalls {
				t.Errorf("stock calls=%d want=%d", stock.calls, wantCalls)
			}
			if env.hooks.markStripeEventCalls != 2+map[bool]int{false: 0, true: 1}[failure == "shipped"] {
				t.Fatal("completed replay did not mark safely processed")
			}
			if failure == "shipped" {
				env.assertStock(t, 3, 3)
			} else {
				env.assertStock(t, 5, 4)
			}
		})
	}
}

func TestRefundAdoptionAndIssuanceGuard(t *testing.T) {
	for _, mode := range []string{"adoption_equivalent", "adoption_superseded", "issuance_equivalent", "issuance_superseded", "issuance_reread_error"} {
		t.Run(mode, func(t *testing.T) {
			env := newTestEnv(t)
			ctx := context.Background()
			paid := env.mustPaidOrder(t)
			id, attempt := "re_1", 1
			if mode == "issuance_equivalent" || mode == "issuance_superseded" || mode == "issuance_reread_error" {
				id = "re_fake_" + paid.ID + "_1"
				env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
			}
			store := refundRaceStore(env, commerce.OrderStatusPaid)
			stock := &flakyStockStore{StockStore: env.service.Stock}
			env.service.Stock = stock
			event := refundWebhookEvent("evt_adopt", "refund.created", payments.Refund{ID: id, OrderID: paid.ID, PaymentIntentID: paid.StripePaymentIntentID, Attempt: attempt, Status: payments.RefundStatusPending})
			finish := startRefundLoser(t, store, func(ctx context.Context) error {
				if mode == "adoption_equivalent" || mode == "adoption_superseded" {
					return env.service.ApplyWebhookEvent(ctx, event)
				}
				_, err := env.service.RefundOrderAs(ctx, paid, commerce.OrderActorAdmin)
				return err
			})
			if err := env.service.ApplyWebhookEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
			if mode == "adoption_superseded" || mode == "issuance_superseded" {
				current := env.mustGetOrder(t, paid.ID)
				current, err := env.commerceStore.TransitionOrder(ctx, current.ID, current.Status, commerce.OrderStatusRefundFailed, commerce.OrderPatch{})
				if err != nil {
					t.Fatal(err)
				}
				id2, attempt2 := "re_2", 2
				if _, err := env.commerceStore.TransitionOrder(ctx, current.ID, current.Status, commerce.OrderStatusRefundPending, commerce.OrderPatch{StripeRefundID: &id2, RefundAttempt: &attempt2}); err != nil {
					t.Fatal(err)
				}
			}
			before := env.mustGetOrder(t, paid.ID)
			effects := refundRaceEffects(env, store, stock)
			injected := errors.New("issuance reread unavailable")
			if mode == "issuance_reread_error" {
				store.getErr = injected
			}
			err := finish()
			store.getErr = nil
			if mode == "adoption_equivalent" || mode == "issuance_equivalent" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, commerce.ErrOrderTransitionConflict) {
				t.Errorf("superseded adoption/issuance = %v", err)
			}
			if mode == "issuance_reread_error" && !errors.Is(err, injected) {
				t.Errorf("issuance lost reread error: %v", err)
			}
			if mode == "adoption_equivalent" {
				effects.markers++
			}
			if got := refundRaceEffects(env, store, stock); got != effects {
				t.Errorf("loser issuance effects=%+v want=%+v", got, effects)
			}
			refundRaceUnchanged(t, env, before)
			wantCreates := 1
			if mode == "adoption_equivalent" || mode == "adoption_superseded" {
				wantCreates = 0
			}
			if env.provider.createRefundCalls != wantCreates {
				t.Fatal("unexpected additional provider refund")
			}
		})
	}
}

func TestRefundEquivalentLoserRepairsStockFailure(t *testing.T) {
	env := newTestEnv(t)
	pending := refundRacePending(t, env)
	stock := &flakyStockStore{StockStore: env.service.Stock, failures: 1}
	env.service.Stock = stock
	store := refundRaceStore(env, commerce.OrderStatusRefundPending)
	loserTime := checkoutTestNow.Add(-time.Hour)
	env.service.Now = func() time.Time { return loserTime }
	finish := startRefundLoser(t, store, func(ctx context.Context) error {
		return env.service.ApplyWebhookEvent(ctx, refundRaceEvent(pending, "evt_loser", payments.RefundStatusSucceeded, ""))
	})
	// The loser generated its timestamp before the mutation intercept. Use a
	// distinct winner time and retain it through the equivalent CAS recovery.
	env.service.Now = func() time.Time { return checkoutTestNow }
	winnerEvent := refundRaceEvent(pending, "evt_winner", payments.RefundStatusSucceeded, "")
	if err := env.service.ApplyWebhookEvent(context.Background(), winnerEvent); !errors.Is(err, ErrStockReleaseFailed) {
		t.Fatal(err)
	}
	committed := env.mustGetOrder(t, pending.ID)
	if !committed.RefundedAt.Equal(checkoutTestNow) || !committed.StockReleasedAt.IsZero() || stock.calls != 1 || env.hooks.markStripeEventCalls != 0 {
		t.Fatal("winner did not commit then fail release without marker")
	}
	emails := len(env.emailSender.Messages())
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	after := env.mustGetOrder(t, pending.ID)
	if !after.RefundedAt.Equal(committed.RefundedAt) || after.StockReleasedAt.IsZero() || len(after.StatusHistory) != len(committed.StatusHistory) || env.metrics.count(observability.MetricCheckoutRefund, "settled") != 1 || len(env.emailSender.Messages()) != emails || stock.calls != 2 || store.claims != 3 || env.hooks.markStripeEventCalls != 1 {
		t.Fatal("equivalent loser did not repair only remaining stock/marker work")
	}
	effects := refundRaceEffects(env, store, stock)
	if err := env.service.ApplyWebhookEvent(context.Background(), winnerEvent); err != nil {
		t.Fatal(err)
	}
	effects.markers++
	if got := refundRaceEffects(env, store, stock); got != effects {
		t.Errorf("repair replay effects=%+v want=%+v", got, effects)
	}
	refundRaceUnchanged(t, env, after)
	env.assertStock(t, 5, 4)
}

func TestRefundFailedSnapshotIssuanceAndAdoption(t *testing.T) {
	for _, issuance := range []bool{false, true} {
		for _, winner := range []string{"equivalent", "different_id_pending", "different_id_failed", "same_id_different_attempt"} {
			t.Run(map[bool]string{false: "adoption/", true: "issuance/"}[issuance]+winner, func(t *testing.T) {
				env := newTestEnv(t)
				ctx := context.Background()
				pending := refundRacePending(t, env)
				failed, err := env.commerceStore.TransitionOrder(ctx, pending.ID, pending.Status, commerce.OrderStatusRefundFailed, commerce.OrderPatch{})
				if err != nil {
					t.Fatal(err)
				}
				requestedID, requestedAttempt := "re_2", 2
				var observed payments.RefundInput
				env.provider.createRefundFn = func(_ context.Context, in payments.RefundInput) (payments.Refund, error) {
					observed = in
					return payments.Refund{ID: requestedID, OrderID: in.OrderID, PaymentIntentID: in.PaymentIntentID, Attempt: in.Attempt, Status: payments.RefundStatusPending}, nil
				}
				store := refundRaceStore(env, commerce.OrderStatusRefundFailed)
				stock := &flakyStockStore{StockStore: env.service.Stock}
				env.service.Stock = stock
				loser := refundWebhookEvent("evt_loser", "refund.created", payments.Refund{ID: requestedID, OrderID: failed.ID, PaymentIntentID: failed.StripePaymentIntentID, Attempt: requestedAttempt, Status: payments.RefundStatusPending})
				finish := startRefundLoser(t, store, func(ctx context.Context) error {
					if !issuance {
						return env.service.ApplyWebhookEvent(ctx, loser)
					}
					_, err := env.service.RefundOrderAs(ctx, failed, commerce.OrderActorAdmin)
					return err
				})
				id, attempt := requestedID, requestedAttempt
				if winner != "equivalent" {
					attempt = 3
				}
				if winner == "different_id_pending" || winner == "different_id_failed" {
					id = "re_3"
				}
				winnerEvent := refundWebhookEvent("evt_winner", "refund.created", payments.Refund{ID: id, OrderID: failed.ID, PaymentIntentID: failed.StripePaymentIntentID, Attempt: attempt, Status: payments.RefundStatusPending})
				if err := env.service.ApplyWebhookEvent(ctx, winnerEvent); err != nil {
					t.Fatal(err)
				}
				if winner == "different_id_failed" {
					current := env.mustGetOrder(t, failed.ID)
					reason := "winner_failure"
					if _, err := env.commerceStore.TransitionOrder(ctx, current.ID, current.Status, commerce.OrderStatusRefundFailed, commerce.OrderPatch{RefundFailureReason: &reason}); err != nil {
						t.Fatal(err)
					}
				}
				before := env.mustGetOrder(t, failed.ID)
				effects := refundRaceEffects(env, store, stock)
				err = finish()
				if winner == "equivalent" {
					if err != nil {
						t.Fatal(err)
					}
					if !issuance {
						effects.markers++
					}
				} else if !errors.Is(err, commerce.ErrOrderTransitionConflict) {
					t.Errorf("superseded failed snapshot=%v", err)
				}
				refundRaceUnchanged(t, env, before)
				if got := refundRaceEffects(env, store, stock); got != effects {
					t.Errorf("failed snapshot loser effects=%+v want=%+v", got, effects)
				}
				if issuance {
					if env.provider.createRefundCalls != 1 || observed.OrderID != failed.ID || observed.PaymentIntentID != failed.StripePaymentIntentID || observed.Attempt != 2 {
						t.Fatalf("refund request changed or repeated: %+v calls=%d", observed, env.provider.createRefundCalls)
					}
				} else if env.provider.createRefundCalls != 0 {
					t.Fatal("webhook adoption created provider refund")
				}
			})
		}
	}
}

func TestRefundLegacyServiceCompatibility(t *testing.T) {
	for _, emptyID := range []bool{false, true} {
		t.Run(map[bool]string{false: "stored_attempt_zero", true: "empty_identity"}[emptyID], func(t *testing.T) {
			env := newTestEnv(t)
			pending := refundRacePending(t, env)
			id, attempt := pending.StripeRefundID, 0
			if emptyID {
				id = ""
			}
			legacy, err := env.commerceStore.PatchOrder(context.Background(), pending.ID, pending.Status, pending.Version, commerce.OrderPatch{StripeRefundID: &id, RefundAttempt: &attempt})
			if err != nil {
				t.Fatal(err)
			}
			event := refundRaceEvent(legacy, "evt_legacy", payments.RefundStatusSucceeded, "")
			if emptyID {
				event.Refund.ID = "re_legacy"
			}
			if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			current := env.mustGetOrder(t, pending.ID)
			if current.Status != commerce.OrderStatusRefunded || current.StripeRefundID != id || current.RefundAttempt != 0 || len(current.StatusHistory) != len(legacy.StatusHistory)+1 || env.metrics.count(observability.MetricCheckoutRefund, "settled") != 1 || env.hooks.markStripeEventCalls != 1 || env.provider.createRefundCalls != 0 {
				t.Fatal("legacy settlement compatibility changed")
			}
			env.assertStock(t, 5, 4)
		})
	}
	t.Run("first_adoption_attempt_zero", func(t *testing.T) {
		env := newTestEnv(t)
		paid := env.mustPaidOrder(t)
		event := refundWebhookEvent("evt_legacy_adopt", "refund.created", payments.Refund{ID: "re_legacy", OrderID: paid.ID, PaymentIntentID: paid.StripePaymentIntentID, Attempt: 0, Status: payments.RefundStatusSucceeded})
		if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		current := env.mustGetOrder(t, paid.ID)
		if current.Status != commerce.OrderStatusRefunded || current.RefundAttempt != 1 || current.StripeRefundID != "re_legacy" || len(current.StatusHistory) != len(paid.StatusHistory)+2 || env.metrics.count(observability.MetricCheckoutRefund, "issued") != 1 || env.metrics.count(observability.MetricCheckoutRefund, "settled") != 1 || env.hooks.markStripeEventCalls != 1 || env.provider.createRefundCalls != 0 {
			t.Fatal("first adoption fallback changed")
		}
		env.assertStock(t, 5, 4)
	})
}

func TestRefundReconcileRefreshIsBestEffortAndStillGuarded(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh_error", true: "refresh_missing"}[missing], func(t *testing.T) {
			env := newTestEnv(t)
			pending := refundRacePending(t, env)
			stock := &flakyStockStore{StockStore: env.service.Stock}
			env.service.Stock = stock
			store := refundRaceStore(env, commerce.OrderStatusRefundPending)
			if missing {
				store.missing = true
			} else {
				store.getErr = errors.New("read unavailable")
			}
			got := env.service.ReconcileRefund(context.Background(), pending)
			if !reflect.DeepEqual(got, pending) || env.provider.getRefundCalls != 0 || stock.calls != 0 || store.claims != 0 {
				t.Fatal("unverified reconciliation performed effects")
			}
		})
	}
	t.Run("refresh_selects_current_refund", func(t *testing.T) {
		env := newTestEnv(t)
		ctx := context.Background()
		paid := env.mustPaidOrder(t)
		env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
		old, err := env.service.RefundOrderAs(ctx, paid, commerce.OrderActorAdmin)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.provider.SettleRefund(old.StripeRefundID, payments.RefundStatusFailed, "old_failure"); err != nil {
			t.Fatal(err)
		}
		if err := env.service.ApplyWebhookEvent(ctx, refundRaceEvent(old, "evt_failed", payments.RefundStatusFailed, "old_failure")); err != nil {
			t.Fatal(err)
		}
		env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
		current, err := env.service.RefundOrderAs(ctx, env.mustGetOrder(t, paid.ID), commerce.OrderActorAdmin)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.provider.SettleRefund(current.StripeRefundID, payments.RefundStatusSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		got := env.service.ReconcileRefund(ctx, old)
		if got.Status != commerce.OrderStatusRefunded || got.StripeRefundID != current.StripeRefundID || got.RefundAttempt != 2 || got.RefundFailureReason != "" || env.provider.createRefundCalls != 2 {
			t.Fatalf("stale reconciliation did not select current refund: %+v", got)
		}
	})
	t.Run("mutation_race_after_refresh", func(t *testing.T) {
		env := newTestEnv(t)
		paid := env.mustPaidOrder(t)
		env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
		pending, err := env.service.RefundOrderAs(context.Background(), paid, commerce.OrderActorAdmin)
		if err != nil {
			t.Fatal(err)
		}
		if err := env.provider.SettleRefund(pending.StripeRefundID, payments.RefundStatusSucceeded, ""); err != nil {
			t.Fatal(err)
		}
		store := refundRaceStore(env, commerce.OrderStatusRefundPending)
		stock := &flakyStockStore{StockStore: env.service.Stock}
		env.service.Stock = stock
		finish := startRefundLoser(t, store, func(ctx context.Context) error { env.service.ReconcileRefund(ctx, pending); return nil })
		current := env.mustGetOrder(t, pending.ID)
		current, err = env.commerceStore.PatchOrder(context.Background(), current.ID, current.Status, current.Version, commerce.OrderPatch{})
		if err != nil {
			t.Fatal(err)
		}
		effects := refundRaceEffects(env, store, stock)
		if err := finish(); err != nil {
			t.Fatal(err)
		}
		refundRaceUnchanged(t, env, current)
		if got := refundRaceEffects(env, store, stock); got != effects {
			t.Fatal("reconciliation blindly retried")
		}
	})
}
