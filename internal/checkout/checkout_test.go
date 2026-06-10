package checkout

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
)

var checkoutTestNow = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

// checkoutTestPasswordHash is an opaque fixture; nothing in this package
// verifies passwords (bcrypt cost 4 shape, never a real credential).
const checkoutTestPasswordHash = "$2a$04$checkout-test-fixture-hash"

const checkoutTestBaseURL = "https://shop.example.test"

func checkoutTestBaseProduct() catalog.Product {
	return catalog.Product{
		ID:            "prod_base",
		Slug:          "carved-coconut-bowl",
		Name:          "Carved Coconut Bowl",
		PriceCents:    1899,
		Status:        catalog.StatusActive,
		SortOrder:     10,
		StockQuantity: 5,
		Version:       1,
		CategorySlugs: []string{"home-decor"},
	}
}

func checkoutTestVariantProduct() catalog.Product {
	return catalog.Product{
		ID:         "prod_variant",
		Slug:       "handwoven-indigo-scarf",
		Name:       "Handwoven Indigo Scarf",
		PriceCents: 3499,
		Status:     catalog.StatusActive,
		SortOrder:  20,
		Version:    1,
		Variants: []catalog.ProductVariant{
			{ID: "var_s", Label: "S", StockQuantity: 1, Status: catalog.StatusActive, SortOrder: 10},
			{ID: "var_m", Label: "M", StockQuantity: 4, Status: catalog.StatusActive, SortOrder: 20},
		},
		CategorySlugs: []string{"textiles"},
	}
}

// checkoutTestLines totals 7297 cents: 2x1899 + 1x3499.
func checkoutTestLines() []commerce.OrderLine {
	return []commerce.OrderLine{
		{
			Slug:           "carved-coconut-bowl",
			ProductID:      "prod_base",
			Name:           "Carved Coconut Bowl",
			UnitPriceCents: 1899,
			Quantity:       2,
			LineTotalCents: 3798,
			ImageURL:       "/images/carved-coconut-bowl.jpg",
		},
		{
			Slug:           "handwoven-indigo-scarf",
			ProductID:      "prod_variant",
			Name:           "Handwoven Indigo Scarf",
			VariantID:      "var_m",
			VariantLabel:   "M",
			UnitPriceCents: 3499,
			Quantity:       1,
			LineTotalCents: 3499,
		},
	}
}

func checkoutTestAddress() commerce.OrderAddress {
	return commerce.OrderAddress{
		FullName:   "Test Shopper",
		Line1:      "123 Sukhumvit Lane",
		City:       "Austin",
		Region:     "TX",
		PostalCode: "78701",
		Country:    "US",
	}
}

type recordingMetrics struct {
	metrics []observability.Metric
}

func (r *recordingMetrics) Record(metric observability.Metric) {
	r.metrics = append(r.metrics, metric)
}

func (r *recordingMetrics) count(name string, outcome string) int {
	total := 0
	for _, metric := range r.metrics {
		if metric.Name != name {
			continue
		}
		for _, dimension := range metric.Dimensions {
			if dimension.Name == "Outcome" && dimension.Value == outcome {
				total++
			}
		}
	}
	return total
}

// stubProvider wraps the fake provider with injectable failures and an
// optional session-status override (the fake provider cannot reach the
// complete-but-unpaid async state on its own).
type stubProvider struct {
	*payments.FakeProvider
	ensureCustomerErr        error
	createPaymentErr         error
	getSessionErr            error
	getSessionStatusOverride string
	ensureCustomerCalls      int
}

func (p *stubProvider) EnsureCustomer(ctx context.Context, customerID string, email string) (string, error) {
	p.ensureCustomerCalls++
	if p.ensureCustomerErr != nil {
		return "", p.ensureCustomerErr
	}
	return p.FakeProvider.EnsureCustomer(ctx, customerID, email)
}

func (p *stubProvider) CreatePaymentSession(ctx context.Context, input payments.PaymentSessionInput) (payments.Session, error) {
	if p.createPaymentErr != nil {
		return payments.Session{}, p.createPaymentErr
	}
	return p.FakeProvider.CreatePaymentSession(ctx, input)
}

func (p *stubProvider) GetSession(ctx context.Context, sessionID string) (payments.Session, error) {
	if p.getSessionErr != nil {
		return payments.Session{}, p.getSessionErr
	}
	session, err := p.FakeProvider.GetSession(ctx, sessionID)
	if err == nil && p.getSessionStatusOverride != "" {
		session.Status = p.getSessionStatusOverride
		session.URL = ""
	}
	return session, err
}

// flakyStockStore fails the first `failures` AdjustStock calls, simulating a
// transient catalog failure between a committed transition and its release.
type flakyStockStore struct {
	catalog.StockStore
	failures int
	calls    int
}

func (f *flakyStockStore) AdjustStock(ctx context.Context, adjustments []catalog.StockAdjustment) error {
	f.calls++
	if f.failures > 0 {
		f.failures--
		return errors.New("stock store is down")
	}
	return f.StockStore.AdjustStock(ctx, adjustments)
}

// hookStore wraps the commerce memory store with injectable failures and
// call counters for the webhook dedupe assertions.
type hookStore struct {
	commerce.Store
	getOrderErr             error
	markStripeEventCalls    int
	transitionConflictOnce  bool
	transitionConflictFired bool
}

func (h *hookStore) GetOrder(ctx context.Context, orderID string) (commerce.Order, bool, error) {
	if h.getOrderErr != nil {
		return commerce.Order{}, false, h.getOrderErr
	}
	return h.Store.GetOrder(ctx, orderID)
}

func (h *hookStore) MarkStripeEventProcessed(ctx context.Context, eventID string, eventType string, orderID string) (bool, error) {
	h.markStripeEventCalls++
	return h.Store.MarkStripeEventProcessed(ctx, eventID, eventType, orderID)
}

// TransitionOrder optionally simulates losing a race: the underlying
// transition is applied (as if a concurrent finalizer won) but the caller
// sees a conflict and must re-read.
func (h *hookStore) TransitionOrder(ctx context.Context, orderID string, from commerce.OrderStatus, to commerce.OrderStatus, patch commerce.OrderPatch) (commerce.Order, error) {
	if h.transitionConflictOnce && !h.transitionConflictFired {
		h.transitionConflictFired = true
		if _, err := h.Store.TransitionOrder(ctx, orderID, from, to, patch); err != nil {
			return commerce.Order{}, err
		}
		return commerce.Order{}, fmt.Errorf("%w: simulated race", commerce.ErrOrderTransitionConflict)
	}
	return h.Store.TransitionOrder(ctx, orderID, from, to, patch)
}

type testEnv struct {
	service       *Service
	commerceStore *commerce.MemoryStore
	hooks         *hookStore
	catalogStore  *catalog.MemoryStore
	provider      *stubProvider
	metrics       *recordingMetrics
	customer      commerce.Customer
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	commerceStore := commerce.NewMemoryStoreWithClock(func() time.Time { return checkoutTestNow })
	hooks := &hookStore{Store: commerceStore}
	catalogStore := catalog.NewMemoryStore(
		[]catalog.Product{checkoutTestBaseProduct(), checkoutTestVariantProduct()},
		nil,
	)
	provider := &stubProvider{FakeProvider: payments.NewFakeProvider()}
	metrics := &recordingMetrics{}

	customer, err := commerceStore.CreateCustomer(context.Background(), "Shopper@example.test", "shopper@example.test", checkoutTestPasswordHash)
	if err != nil {
		t.Fatalf("CreateCustomer returned error: %v", err)
	}

	return &testEnv{
		service: &Service{
			Commerce: hooks,
			Payments: provider,
			Stock:    catalogStore,
			Metrics:  metrics,
			BaseURL:  checkoutTestBaseURL + "/",
			Now:      func() time.Time { return checkoutTestNow },
		},
		commerceStore: commerceStore,
		hooks:         hooks,
		catalogStore:  catalogStore,
		provider:      provider,
		metrics:       metrics,
		customer:      customer,
	}
}

func (env *testEnv) refreshCustomer(t *testing.T) commerce.Customer {
	t.Helper()
	customer, found, err := env.commerceStore.GetCustomerByID(context.Background(), env.customer.ID)
	if err != nil || !found {
		t.Fatalf("GetCustomerByID(%q) = found %t, err %v", env.customer.ID, found, err)
	}
	return customer
}

func (env *testEnv) currentCart(t *testing.T) commerce.CartRecord {
	t.Helper()
	record, found, err := env.commerceStore.GetCart(context.Background(), env.customer.ID)
	if err != nil {
		t.Fatalf("GetCart returned error: %v", err)
	}
	if !found {
		return commerce.CartRecord{CustomerID: env.customer.ID}
	}
	return record
}

func (env *testEnv) placeOrderInput(t *testing.T, lines []commerce.OrderLine) PlaceOrderInput {
	t.Helper()
	cartLines := make([]cart.Line, 0, len(lines))
	for _, line := range lines {
		cartLines = append(cartLines, cart.Line{Slug: line.Slug, VariantID: line.VariantID, Quantity: line.Quantity})
	}
	record := env.currentCart(t)
	record.Lines = cartLines
	return PlaceOrderInput{
		Customer:  env.refreshCustomer(t),
		Email:     "shopper@example.test",
		AddressID: "addr00000000000000000000ab",
		Address:   checkoutTestAddress(),
		Lines:     lines,
		Cart:      record,
	}
}

func (env *testEnv) mustPlaceOrder(t *testing.T, lines []commerce.OrderLine) (string, commerce.Order) {
	t.Helper()
	redirectURL, order, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, lines))
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}
	return redirectURL, order
}

func (env *testEnv) mustGetOrder(t *testing.T, orderID string) commerce.Order {
	t.Helper()
	order, found, err := env.commerceStore.GetOrder(context.Background(), orderID)
	if err != nil || !found {
		t.Fatalf("GetOrder(%q) = found %t, err %v", orderID, found, err)
	}
	return order
}

func (env *testEnv) orderCount(t *testing.T) int {
	t.Helper()
	orders, err := env.commerceStore.ListOrders(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListOrders returned error: %v", err)
	}
	return len(orders)
}

func (env *testEnv) assertStock(t *testing.T, wantBase int, wantVarM int) {
	t.Helper()
	base, found, err := env.catalogStore.GetProductByID(context.Background(), "prod_base")
	if err != nil || !found {
		t.Fatalf("GetProductByID(prod_base) = found %t, err %v", found, err)
	}
	if base.StockQuantity != wantBase {
		t.Errorf("prod_base stock = %d, want %d", base.StockQuantity, wantBase)
	}
	variant, found, err := env.catalogStore.GetProductByID(context.Background(), "prod_variant")
	if err != nil || !found {
		t.Fatalf("GetProductByID(prod_variant) = found %t, err %v", found, err)
	}
	stock, ok := variant.AvailableStockForVariant("var_m")
	if !ok {
		t.Fatalf("var_m not found on prod_variant")
	}
	if stock != wantVarM {
		t.Errorf("prod_variant var_m stock = %d, want %d", stock, wantVarM)
	}
}

// paidSession marks the fake session paid and returns the provider's view of
// it, the shape the confirm page passes to FinalizePayment.
func (env *testEnv) paidSession(t *testing.T, sessionID string) payments.Session {
	t.Helper()
	if _, err := env.provider.MarkSessionPaid(sessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid(%q) returned error: %v", sessionID, err)
	}
	session, err := env.provider.GetSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetSession(%q) returned error: %v", sessionID, err)
	}
	return session
}

func TestPlaceOrderCreatesPendingOrderAndSession(t *testing.T) {
	env := newTestEnv(t)
	redirectURL, order := env.mustPlaceOrder(t, checkoutTestLines())

	wantURL := "/checkout/fake-pay?session_id=cs_fake_" + order.ID
	if redirectURL != wantURL {
		t.Errorf("redirect URL = %q, want %q", redirectURL, wantURL)
	}
	if order.Status != commerce.OrderStatusPendingPayment {
		t.Errorf("order status = %q, want %q", order.Status, commerce.OrderStatusPendingPayment)
	}
	if order.SubtotalCents != 7297 || order.ShippingCents != 0 || order.TaxCents != 0 || order.TotalCents != 7297 {
		t.Errorf("order totals = subtotal %d shipping %d tax %d total %d, want 7297/0/0/7297",
			order.SubtotalCents, order.ShippingCents, order.TaxCents, order.TotalCents)
	}
	if order.Currency != "usd" {
		t.Errorf("order currency = %q, want %q", order.Currency, "usd")
	}
	if order.CheckoutAttempt != 1 {
		t.Errorf("order checkout attempt = %d, want 1", order.CheckoutAttempt)
	}
	if order.StripeCheckoutSessionID != "cs_fake_"+order.ID {
		t.Errorf("order checkout session id = %q, want %q", order.StripeCheckoutSessionID, "cs_fake_"+order.ID)
	}
	wantFingerprint := env.service.Fingerprint(checkoutTestLines(), "addr00000000000000000000ab")
	if order.CartFingerprint != wantFingerprint {
		t.Errorf("order fingerprint = %q, want %q", order.CartFingerprint, wantFingerprint)
	}
	if len(order.Lines) != 2 || order.Lines[0].Name != "Carved Coconut Bowl" || order.Lines[1].VariantLabel != "M" {
		t.Errorf("order lines snapshot = %+v, want the two priced input lines", order.Lines)
	}
	if order.ShippingAddress != checkoutTestAddress() {
		t.Errorf("order shipping address = %+v, want %+v", order.ShippingAddress, checkoutTestAddress())
	}

	env.assertStock(t, 3, 3)

	persisted := env.mustGetOrder(t, order.ID)
	if persisted.StripeCheckoutSessionID != order.StripeCheckoutSessionID {
		t.Errorf("persisted session id = %q, want %q", persisted.StripeCheckoutSessionID, order.StripeCheckoutSessionID)
	}

	record := env.currentCart(t)
	if record.PendingOrderID != order.ID {
		t.Errorf("cart pending order id = %q, want %q", record.PendingOrderID, order.ID)
	}
	if record.PendingFingerprint != wantFingerprint {
		t.Errorf("cart pending fingerprint = %q, want %q", record.PendingFingerprint, wantFingerprint)
	}
	if len(record.Lines) != 2 {
		t.Errorf("cart lines after place order = %d, want 2 (cart is not cleared until payment)", len(record.Lines))
	}

	customer := env.refreshCustomer(t)
	if customer.StripeCustomerID != "cus_fake_"+customer.ID {
		t.Errorf("customer stripe id = %q, want %q", customer.StripeCustomerID, "cus_fake_"+customer.ID)
	}
	if env.provider.ensureCustomerCalls != 1 {
		t.Errorf("EnsureCustomer calls = %d, want 1", env.provider.ensureCustomerCalls)
	}
}

func TestPlaceOrderValidatesInput(t *testing.T) {
	env := newTestEnv(t)
	tests := []struct {
		name  string
		input PlaceOrderInput
	}{
		{name: "missing customer", input: PlaceOrderInput{Lines: checkoutTestLines()}},
		{name: "no lines", input: PlaceOrderInput{Customer: env.customer}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := env.service.PlaceOrder(context.Background(), test.input); err == nil {
				t.Fatalf("PlaceOrder accepted invalid input")
			}
			if count := env.orderCount(t); count != 0 {
				t.Errorf("order count = %d, want 0", count)
			}
		})
	}
}

func TestPlaceOrderIdempotentReentrySameFingerprint(t *testing.T) {
	env := newTestEnv(t)
	firstURL, first := env.mustPlaceOrder(t, checkoutTestLines())
	secondURL, second := env.mustPlaceOrder(t, checkoutTestLines())

	if secondURL != firstURL {
		t.Errorf("re-entry URL = %q, want the original %q", secondURL, firstURL)
	}
	if second.ID != first.ID {
		t.Errorf("re-entry order id = %q, want the original %q", second.ID, first.ID)
	}
	if count := env.orderCount(t); count != 1 {
		t.Errorf("order count = %d, want 1 (no duplicate order)", count)
	}
	// Stock reserved exactly once.
	env.assertStock(t, 3, 3)
}

func TestPlaceOrderReentryFingerprintMismatchCancelsStaleOrder(t *testing.T) {
	env := newTestEnv(t)
	_, first := env.mustPlaceOrder(t, checkoutTestLines())

	changed := checkoutTestLines()
	changed[0].Quantity = 1
	changed[0].LineTotalCents = 1899
	_, second, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, changed))
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}

	if second.ID == first.ID {
		t.Fatalf("expected a fresh order, got the stale one %q", first.ID)
	}
	stale := env.mustGetOrder(t, first.ID)
	if stale.Status != commerce.OrderStatusCanceled {
		t.Errorf("stale order status = %q, want %q", stale.Status, commerce.OrderStatusCanceled)
	}
	if got := stale.StatusHistory[len(stale.StatusHistory)-1].Actor; got != commerce.OrderActorSystem {
		t.Errorf("stale order cancel actor = %q, want %q", got, commerce.OrderActorSystem)
	}
	// Old reservation (2 base, 1 var_m) released; new one (1 base, 1 var_m) held.
	env.assertStock(t, 4, 3)
	if second.TotalCents != 1899+3499 {
		t.Errorf("new order total = %d, want %d", second.TotalCents, 1899+3499)
	}
	record := env.currentCart(t)
	if record.PendingOrderID != second.ID {
		t.Errorf("cart pending order id = %q, want the fresh order %q", record.PendingOrderID, second.ID)
	}
}

func TestPlaceOrderReentryPointerToMissingOrderStartsFresh(t *testing.T) {
	env := newTestEnv(t)
	input := env.placeOrderInput(t, checkoutTestLines())
	input.Cart.PendingOrderID = "00000000000000000000000000"
	input.Cart.PendingFingerprint = "stale"

	_, order, err := env.service.PlaceOrder(context.Background(), input)
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}
	if order.Status != commerce.OrderStatusPendingPayment {
		t.Errorf("order status = %q, want %q", order.Status, commerce.OrderStatusPendingPayment)
	}
	if count := env.orderCount(t); count != 1 {
		t.Errorf("order count = %d, want 1", count)
	}
}

func TestPlaceOrderReentryPointerToPaidOrderIsNotCanceled(t *testing.T) {
	env := newTestEnv(t)
	_, first := env.mustPlaceOrder(t, checkoutTestLines())
	if _, err := env.commerceStore.TransitionOrder(context.Background(), first.ID, commerce.OrderStatusPendingPayment, commerce.OrderStatusPaid, commerce.OrderPatch{Actor: commerce.OrderActorStripe}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}

	_, second, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, checkoutTestLines()))
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected a fresh order, got the paid one")
	}
	if got := env.mustGetOrder(t, first.ID).Status; got != commerce.OrderStatusPaid {
		t.Errorf("paid order status = %q, want it untouched as %q", got, commerce.OrderStatusPaid)
	}
	// Paid order keeps its reservation; the new order reserves again.
	env.assertStock(t, 1, 2)
}

func TestPlaceOrderReentryPaidSessionRoutesToConfirm(t *testing.T) {
	env := newTestEnv(t)
	_, first := env.mustPlaceOrder(t, checkoutTestLines())
	if _, err := env.provider.MarkSessionPaid(first.StripeCheckoutSessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	redirectURL, order, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, checkoutTestLines()))
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}
	wantURL := checkoutTestBaseURL + "/checkout/confirm?session_id=" + first.StripeCheckoutSessionID
	if redirectURL != wantURL {
		t.Errorf("redirect URL = %q, want the confirm reconcile path %q", redirectURL, wantURL)
	}
	if order.ID != first.ID {
		t.Errorf("order id = %q, want the original %q", order.ID, first.ID)
	}
	if count := env.orderCount(t); count != 1 {
		t.Errorf("order count = %d, want 1", count)
	}
	env.assertStock(t, 3, 3)
}

func TestPlaceOrderInsufficientStockReturnsTypedError(t *testing.T) {
	env := newTestEnv(t)
	lines := checkoutTestLines()
	lines[0].Quantity = 6
	lines[0].LineTotalCents = 6 * 1899

	_, _, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, lines))
	var insufficient catalog.InsufficientStockError
	if !errors.As(err, &insufficient) {
		t.Fatalf("PlaceOrder error = %v, want catalog.InsufficientStockError", err)
	}
	if insufficient.ProductID != "prod_base" || insufficient.Available != 5 {
		t.Errorf("insufficient stock detail = %+v, want prod_base with 5 available", insufficient)
	}
	if count := env.orderCount(t); count != 0 {
		t.Errorf("order count = %d, want 0", count)
	}
	env.assertStock(t, 5, 4)
	if got := env.metrics.count(observability.MetricCheckoutPayment, "insufficient_stock"); got != 1 {
		t.Errorf("insufficient_stock metric count = %d, want 1", got)
	}
}

func TestPlaceOrderProviderFailureCompensates(t *testing.T) {
	tests := []struct {
		name      string
		configure func(provider *stubProvider)
	}{
		{
			name:      "create payment session fails",
			configure: func(provider *stubProvider) { provider.createPaymentErr = errors.New("stripe is down") },
		},
		{
			name:      "ensure customer fails",
			configure: func(provider *stubProvider) { provider.ensureCustomerErr = errors.New("stripe is down") },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t)
			test.configure(env.provider)

			_, _, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, checkoutTestLines()))
			if err == nil {
				t.Fatalf("PlaceOrder succeeded, want a provider error")
			}

			// Reservation released; order canceled by the system actor.
			env.assertStock(t, 5, 4)
			orders, listErr := env.commerceStore.ListOrders(context.Background(), 0)
			if listErr != nil {
				t.Fatalf("ListOrders returned error: %v", listErr)
			}
			if len(orders) != 1 {
				t.Fatalf("order count = %d, want 1 compensated order", len(orders))
			}
			if orders[0].Status != commerce.OrderStatusCanceled {
				t.Errorf("order status = %q, want %q", orders[0].Status, commerce.OrderStatusCanceled)
			}
			if got := orders[0].StatusHistory[len(orders[0].StatusHistory)-1].Actor; got != commerce.OrderActorSystem {
				t.Errorf("cancel actor = %q, want %q", got, commerce.OrderActorSystem)
			}
			if record := env.currentCart(t); record.PendingOrderID != "" {
				t.Errorf("cart pending order id = %q, want empty after compensation", record.PendingOrderID)
			}
			if got := env.metrics.count(observability.MetricCheckoutPayment, "provider_error"); got != 1 {
				t.Errorf("provider_error metric count = %d, want 1", got)
			}
		})
	}
}

func TestPlaceOrderSkipsEnsureCustomerWhenStripeIDExists(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.commerceStore.SetStripeCustomerID(context.Background(), env.customer.ID, "cus_existing"); err != nil {
		t.Fatalf("SetStripeCustomerID returned error: %v", err)
	}

	env.mustPlaceOrder(t, checkoutTestLines())
	if env.provider.ensureCustomerCalls != 0 {
		t.Errorf("EnsureCustomer calls = %d, want 0 when the stripe id already exists", env.provider.ensureCustomerCalls)
	}
}

func TestFinalizePaymentMarksOrderPaidAndClearsCart(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)

	finalized, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}
	if finalized.Status != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want %q", finalized.Status, commerce.OrderStatusPaid)
	}
	if finalized.StripePaymentIntentID != "pi_fake_"+order.ID {
		t.Errorf("payment intent id = %q, want %q", finalized.StripePaymentIntentID, "pi_fake_"+order.ID)
	}
	if finalized.PaymentCardBrand != "visa" || finalized.PaymentCardLast4 != "4242" {
		t.Errorf("card display = %q/%q, want visa/4242", finalized.PaymentCardBrand, finalized.PaymentCardLast4)
	}
	if !finalized.PaidAt.Equal(checkoutTestNow) {
		t.Errorf("paid at = %v, want the injected clock %v", finalized.PaidAt, checkoutTestNow)
	}
	if got := finalized.StatusHistory[len(finalized.StatusHistory)-1].Actor; got != commerce.OrderActorStripe {
		t.Errorf("paid actor = %q, want %q", got, commerce.OrderActorStripe)
	}

	record := env.currentCart(t)
	if len(record.Lines) != 0 || record.PendingOrderID != "" || record.PendingFingerprint != "" {
		t.Errorf("cart after finalize = %+v, want empty lines and cleared pointer", record)
	}
	// Paid order keeps its reservation.
	env.assertStock(t, 3, 3)
	if got := env.metrics.count(observability.MetricCheckoutPayment, "success"); got != 1 {
		t.Errorf("success metric count = %d, want 1", got)
	}
}

func TestFinalizePaymentReplayIsNoOp(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)

	first, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("first FinalizePayment returned error: %v", err)
	}
	second, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("replayed FinalizePayment returned error: %v", err)
	}
	if second.Version != first.Version {
		t.Errorf("replay bumped version %d -> %d, want a no-op", first.Version, second.Version)
	}
	if len(second.StatusHistory) != len(first.StatusHistory) {
		t.Errorf("replay appended history (%d -> %d entries), want a no-op", len(first.StatusHistory), len(second.StatusHistory))
	}
	if got := env.metrics.count(observability.MetricCheckoutPayment, "success"); got != 1 {
		t.Errorf("success metric count = %d, want 1 (replays are not successes)", got)
	}
}

func TestFinalizePaymentRejectsBadSessions(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())

	unpaid, err := env.provider.GetSession(context.Background(), order.StripeCheckoutSessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	mismatched := env.paidSession(t, order.StripeCheckoutSessionID)
	mismatched.OrderID = "00000000000000000000000000"

	tests := []struct {
		name    string
		session payments.Session
	}{
		{name: "session not paid", session: unpaid},
		{name: "session belongs to another order", session: mismatched},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := env.service.FinalizePayment(context.Background(), order.ID, test.session); err == nil {
				t.Fatalf("FinalizePayment accepted a bad session")
			}
			if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPendingPayment {
				t.Errorf("order status = %q, want it left %q", got, commerce.OrderStatusPendingPayment)
			}
		})
	}
}

func TestFinalizePaymentLostRaceRereadsWinner(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	env.hooks.transitionConflictOnce = true

	finalized, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}
	if finalized.Status != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want %q", finalized.Status, commerce.OrderStatusPaid)
	}
	if record := env.currentCart(t); record.PendingOrderID != "" || len(record.Lines) != 0 {
		t.Errorf("cart after lost-race finalize = %+v, want cleared", record)
	}
	if got := env.metrics.count(observability.MetricCheckoutPayment, "error"); got != 0 {
		t.Errorf("error metric count = %d, want 0 (a lost race to paid is success)", got)
	}
}

func TestApplyWebhookEventCompletedFinalizesOrder(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	event := payments.Event{
		ID:        "evt_completed_1",
		Type:      "checkout.session.completed",
		SessionID: session.ID,
		OrderID:   order.ID,
		Session:   session,
	}

	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	paid := env.mustGetOrder(t, order.ID)
	if paid.Status != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want %q", paid.Status, commerce.OrderStatusPaid)
	}
	if env.hooks.markStripeEventCalls != 1 {
		t.Errorf("MarkStripeEventProcessed calls = %d, want 1", env.hooks.markStripeEventCalls)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "processed"); got != 1 {
		t.Errorf("processed metric count = %d, want 1", got)
	}

	// Duplicate delivery collapses to a no-op.
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("duplicate ApplyWebhookEvent returned error: %v", err)
	}
	replayed := env.mustGetOrder(t, order.ID)
	if replayed.Version != paid.Version || len(replayed.StatusHistory) != len(paid.StatusHistory) {
		t.Errorf("duplicate event mutated the order: version %d -> %d, history %d -> %d",
			paid.Version, replayed.Version, len(paid.StatusHistory), len(replayed.StatusHistory))
	}
	env.assertStock(t, 3, 3)
}

func TestApplyWebhookEventWebhookPayloadWithoutCardFieldsExpands(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	full := env.paidSession(t, order.StripeCheckoutSessionID)

	// Stripe webhook payloads carry the payment intent as a bare ID and no
	// card fields; the finalize path must expand them via GetSession.
	bare := full
	bare.CardBrand = ""
	bare.CardLast4 = ""
	event := payments.Event{ID: "evt_bare_1", Type: "checkout.session.completed", SessionID: bare.ID, OrderID: order.ID, Session: bare}

	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	paid := env.mustGetOrder(t, order.ID)
	if paid.PaymentCardBrand != "visa" || paid.PaymentCardLast4 != "4242" {
		t.Errorf("card display = %q/%q, want visa/4242 expanded from GetSession", paid.PaymentCardBrand, paid.PaymentCardLast4)
	}
}

func TestApplyWebhookEventExpiredReleasesStock(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session, err := env.provider.GetSession(context.Background(), order.StripeCheckoutSessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	event := payments.Event{ID: "evt_expired_1", Type: "checkout.session.expired", SessionID: session.ID, OrderID: order.ID, Session: session}

	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	expired := env.mustGetOrder(t, order.ID)
	if expired.Status != commerce.OrderStatusExpired {
		t.Errorf("order status = %q, want %q", expired.Status, commerce.OrderStatusExpired)
	}
	if got := expired.StatusHistory[len(expired.StatusHistory)-1].Actor; got != commerce.OrderActorStripe {
		t.Errorf("expired actor = %q, want %q", got, commerce.OrderActorStripe)
	}
	env.assertStock(t, 5, 4)

	// Duplicate delivery must not release the stock a second time.
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("duplicate ApplyWebhookEvent returned error: %v", err)
	}
	env.assertStock(t, 5, 4)
	if got := len(env.mustGetOrder(t, order.ID).StatusHistory); got != 2 {
		t.Errorf("status history length = %d, want 2", got)
	}
}

func TestApplyWebhookEventAsyncPaymentFailedReleasesStock(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	event := payments.Event{ID: "evt_failed_1", Type: "checkout.session.async_payment_failed", OrderID: order.ID}

	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPaymentFailed {
		t.Errorf("order status = %q, want %q", got, commerce.OrderStatusPaymentFailed)
	}
	env.assertStock(t, 5, 4)
}

func TestApplyWebhookEventAsyncPaymentSucceededFinalizes(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	event := payments.Event{ID: "evt_async_1", Type: "checkout.session.async_payment_succeeded", SessionID: session.ID, OrderID: order.ID, Session: session}

	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want %q", got, commerce.OrderStatusPaid)
	}
}

func TestApplyWebhookEventNeverDowngradesPaid(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	if _, err := env.service.FinalizePayment(context.Background(), order.ID, session); err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}

	for _, eventType := range []string{"checkout.session.expired", "checkout.session.async_payment_failed"} {
		event := payments.Event{ID: "evt_late_" + eventType, Type: eventType, OrderID: order.ID, Session: session}
		if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
			t.Fatalf("ApplyWebhookEvent(%s) returned error: %v", eventType, err)
		}
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want paid never downgraded", got)
	}
	// Paid reservation must not be released by late failure events.
	env.assertStock(t, 3, 3)
}

func TestApplyWebhookEventAmountMismatchLeavesOrderPending(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	session.AmountTotalCents = order.TotalCents - 100
	event := payments.Event{ID: "evt_mismatch_1", Type: "checkout.session.completed", SessionID: session.ID, OrderID: order.ID, Session: session}

	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v (want a 200-equivalent nil)", err)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPendingPayment {
		t.Errorf("order status = %q, want it left %q", got, commerce.OrderStatusPendingPayment)
	}
	if env.hooks.markStripeEventCalls != 0 {
		t.Errorf("MarkStripeEventProcessed calls = %d, want 0 (mismatch is not successful processing)", env.hooks.markStripeEventCalls)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "amount_mismatch"); got != 1 {
		t.Errorf("amount_mismatch metric count = %d, want 1", got)
	}
	env.assertStock(t, 3, 3)
}

func TestApplyWebhookEventIgnoredCases(t *testing.T) {
	tests := []struct {
		name      string
		event     func(env *testEnv, order commerce.Order) payments.Event
		wantMarks int
	}{
		{
			name: "unknown event type",
			event: func(env *testEnv, order commerce.Order) payments.Event {
				return payments.Event{ID: "evt_other", Type: "payment_intent.created", OrderID: order.ID}
			},
			wantMarks: 0,
		},
		{
			name: "no order id",
			event: func(env *testEnv, order commerce.Order) payments.Event {
				return payments.Event{ID: "evt_no_order", Type: "checkout.session.completed", Session: payments.Session{Mode: "payment", PaymentStatus: "paid"}}
			},
			wantMarks: 0,
		},
		{
			name: "unknown order",
			event: func(env *testEnv, order commerce.Order) payments.Event {
				return payments.Event{ID: "evt_unknown_order", Type: "checkout.session.expired", OrderID: "00000000000000000000000000"}
			},
			wantMarks: 0,
		},
		{
			name: "setup mode completed",
			event: func(env *testEnv, order commerce.Order) payments.Event {
				return payments.Event{ID: "evt_setup", Type: "checkout.session.completed", Session: payments.Session{Mode: "setup", PaymentStatus: "no_payment_required"}}
			},
			wantMarks: 1,
		},
		{
			name: "completed but not yet paid",
			event: func(env *testEnv, order commerce.Order) payments.Event {
				return payments.Event{ID: "evt_unpaid", Type: "checkout.session.completed", OrderID: order.ID, Session: payments.Session{Mode: "payment", PaymentStatus: "unpaid", AmountTotalCents: order.TotalCents}}
			},
			wantMarks: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t)
			_, order := env.mustPlaceOrder(t, checkoutTestLines())

			if err := env.service.ApplyWebhookEvent(context.Background(), test.event(env, order)); err != nil {
				t.Fatalf("ApplyWebhookEvent returned error: %v", err)
			}
			if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPendingPayment {
				t.Errorf("order status = %q, want it left %q", got, commerce.OrderStatusPendingPayment)
			}
			if env.hooks.markStripeEventCalls != test.wantMarks {
				t.Errorf("MarkStripeEventProcessed calls = %d, want %d", env.hooks.markStripeEventCalls, test.wantMarks)
			}
			if got := env.metrics.count(observability.MetricStripeWebhook, "ignored"); got != 1 {
				t.Errorf("ignored metric count = %d, want 1", got)
			}
			env.assertStock(t, 3, 3)
		})
	}
}

func TestApplyWebhookEventStoreErrorPropagates(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	storeErr := errors.New("dynamo is down")
	env.hooks.getOrderErr = storeErr

	event := payments.Event{ID: "evt_store_err", Type: "checkout.session.expired", OrderID: order.ID}
	if err := env.service.ApplyWebhookEvent(context.Background(), event); !errors.Is(err, storeErr) {
		t.Fatalf("ApplyWebhookEvent error = %v, want the transient store error to bubble for a 500", err)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "error"); got != 1 {
		t.Errorf("error metric count = %d, want 1", got)
	}
}

func TestCancelPendingOrder(t *testing.T) {
	t.Run("pending order is canceled and stock released", func(t *testing.T) {
		env := newTestEnv(t)
		_, order := env.mustPlaceOrder(t, checkoutTestLines())

		if err := env.service.CancelPendingOrder(context.Background(), env.mustGetOrder(t, order.ID)); err != nil {
			t.Fatalf("CancelPendingOrder returned error: %v", err)
		}
		canceled := env.mustGetOrder(t, order.ID)
		if canceled.Status != commerce.OrderStatusCanceled {
			t.Errorf("order status = %q, want %q", canceled.Status, commerce.OrderStatusCanceled)
		}
		if got := canceled.StatusHistory[len(canceled.StatusHistory)-1].Actor; got != commerce.OrderActorSystem {
			t.Errorf("cancel actor = %q, want %q", got, commerce.OrderActorSystem)
		}
		env.assertStock(t, 5, 4)

		// Canceling again is a no-op and must not double-release.
		if err := env.service.CancelPendingOrder(context.Background(), canceled); err != nil {
			t.Fatalf("repeat CancelPendingOrder returned error: %v", err)
		}
		env.assertStock(t, 5, 4)
	})

	t.Run("paid order is refused", func(t *testing.T) {
		env := newTestEnv(t)
		_, order := env.mustPlaceOrder(t, checkoutTestLines())
		session := env.paidSession(t, order.StripeCheckoutSessionID)
		if _, err := env.service.FinalizePayment(context.Background(), order.ID, session); err != nil {
			t.Fatalf("FinalizePayment returned error: %v", err)
		}

		err := env.service.CancelPendingOrder(context.Background(), env.mustGetOrder(t, order.ID))
		if err == nil || !strings.Contains(err.Error(), "cannot cancel") {
			t.Fatalf("CancelPendingOrder error = %v, want a refusal for paid orders", err)
		}
		if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPaid {
			t.Errorf("order status = %q, want it left %q", got, commerce.OrderStatusPaid)
		}
		env.assertStock(t, 3, 3)
	})
}

func TestCancelPendingOrderExpiresSessionSoItCannotBePaid(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())

	if err := env.service.CancelPendingOrder(context.Background(), env.mustGetOrder(t, order.ID)); err != nil {
		t.Fatalf("CancelPendingOrder returned error: %v", err)
	}
	canceled := env.mustGetOrder(t, order.ID)
	if canceled.Status != commerce.OrderStatusCanceled {
		t.Errorf("order status = %q, want %q", canceled.Status, commerce.OrderStatusCanceled)
	}
	if canceled.StockReleasedAt.IsZero() {
		t.Errorf("StockReleasedAt is zero, want the release claim recorded")
	}
	env.assertStock(t, 5, 4)

	session, err := env.provider.GetSession(context.Background(), order.StripeCheckoutSessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.Status != payments.SessionStatusExpired {
		t.Errorf("session status = %q, want %q", session.Status, payments.SessionStatusExpired)
	}
	// The §7.1 disaster scenario: a stale checkout tab can no longer pay the
	// canceled order.
	if _, err := env.provider.MarkSessionPaid(order.StripeCheckoutSessionID, false); !errors.Is(err, payments.ErrSessionExpired) {
		t.Errorf("MarkSessionPaid on the canceled order's session error = %v, want %v", err, payments.ErrSessionExpired)
	}
}

func TestPlaceOrderReentryFingerprintMismatchExpiresStaleSession(t *testing.T) {
	env := newTestEnv(t)
	_, first := env.mustPlaceOrder(t, checkoutTestLines())

	changed := checkoutTestLines()
	changed[0].Quantity = 1
	changed[0].LineTotalCents = 1899
	if _, _, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, changed)); err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}

	if _, err := env.provider.MarkSessionPaid(first.StripeCheckoutSessionID, false); !errors.Is(err, payments.ErrSessionExpired) {
		t.Errorf("MarkSessionPaid on the stale order's session error = %v, want %v", err, payments.ErrSessionExpired)
	}
}

func TestCancelPendingOrderPaidSessionFinalizesInstead(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	env.paidSession(t, order.StripeCheckoutSessionID)

	err := env.service.CancelPendingOrder(context.Background(), env.mustGetOrder(t, order.ID))
	if !errors.Is(err, ErrOrderPaidNotCanceled) {
		t.Fatalf("CancelPendingOrder error = %v, want %v", err, ErrOrderPaidNotCanceled)
	}
	paid := env.mustGetOrder(t, order.ID)
	if paid.Status != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want %q (never cancel paid-for goods)", paid.Status, commerce.OrderStatusPaid)
	}
	if paid.StripePaymentIntentID != "pi_fake_"+order.ID {
		t.Errorf("payment intent id = %q, want the finalize patch applied", paid.StripePaymentIntentID)
	}
	// Paid order keeps its reservation; finalize cleared the cart + pointer.
	env.assertStock(t, 3, 3)
	if record := env.currentCart(t); record.PendingOrderID != "" || len(record.Lines) != 0 {
		t.Errorf("cart after finalize-instead-of-cancel = %+v, want cleared", record)
	}
}

func TestPlaceOrderReentryMismatchWithPaidStaleOrderFinalizesAndPlacesFresh(t *testing.T) {
	env := newTestEnv(t)
	_, first := env.mustPlaceOrder(t, checkoutTestLines())
	if _, err := env.provider.MarkSessionPaid(first.StripeCheckoutSessionID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	changed := checkoutTestLines()
	changed[0].Quantity = 1
	changed[0].LineTotalCents = 1899
	_, second, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, changed))
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}

	if second.ID == first.ID {
		t.Fatalf("expected a fresh order, got the paid one %q", first.ID)
	}
	if got := env.mustGetOrder(t, first.ID).Status; got != commerce.OrderStatusPaid {
		t.Errorf("stale order status = %q, want %q (payment landed mid-cancel)", got, commerce.OrderStatusPaid)
	}
	if got := env.mustGetOrder(t, second.ID).Status; got != commerce.OrderStatusPendingPayment {
		t.Errorf("fresh order status = %q, want %q", got, commerce.OrderStatusPendingPayment)
	}
	// First order's reservation is kept (paid); the fresh order reserves on top.
	env.assertStock(t, 2, 2)
	if record := env.currentCart(t); record.PendingOrderID != second.ID {
		t.Errorf("cart pending order id = %q, want the fresh order %q", record.PendingOrderID, second.ID)
	}
}

func TestCancelPendingOrderPaymentInFlightRefusesCancel(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	// Async payment in flight: the session completed but is not yet paid, and
	// the provider refuses to expire completed sessions.
	env.provider.getSessionStatusOverride = payments.SessionStatusComplete

	err := env.service.CancelPendingOrder(context.Background(), env.mustGetOrder(t, order.ID))
	if !errors.Is(err, ErrPaymentSessionInFlight) {
		t.Fatalf("CancelPendingOrder error = %v, want %v", err, ErrPaymentSessionInFlight)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPendingPayment {
		t.Errorf("order status = %q, want it left %q for the webhook to resolve", got, commerce.OrderStatusPendingPayment)
	}
	env.assertStock(t, 3, 3)

	env.provider.getSessionStatusOverride = ""
	session, err := env.provider.GetSession(context.Background(), order.StripeCheckoutSessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.Status != payments.SessionStatusOpen {
		t.Errorf("session status = %q, want %q (expire must not have been attempted)", session.Status, payments.SessionStatusOpen)
	}
}

func TestApplyWebhookEventPaidAfterTerminalAcksWithMetric(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	// Simulate a cancel that raced the payment without expiring the session.
	if _, err := env.commerceStore.TransitionOrder(context.Background(), order.ID, commerce.OrderStatusPendingPayment, commerce.OrderStatusCanceled, commerce.OrderPatch{Actor: commerce.OrderActorAdmin}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}

	if _, err := env.service.FinalizePayment(context.Background(), order.ID, session); !errors.Is(err, ErrOrderNotFinalizable) {
		t.Fatalf("FinalizePayment error = %v, want %v", err, ErrOrderNotFinalizable)
	}

	event := payments.Event{ID: "evt_terminal_1", Type: "checkout.session.completed", SessionID: session.ID, OrderID: order.ID, Session: session}
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent error = %v, want nil (a retry can never reconcile a terminal order)", err)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusCanceled {
		t.Errorf("order status = %q, want it left %q", got, commerce.OrderStatusCanceled)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "paid_after_terminal"); got != 1 {
		t.Errorf("paid_after_terminal metric count = %d, want 1", got)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "error"); got != 0 {
		t.Errorf("error metric count = %d, want 0 (this is an ops condition, not a transient failure)", got)
	}
}

func TestApplyWebhookEventReleaseFailureIsRetriedAndReleasesExactlyOnce(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	flaky := &flakyStockStore{StockStore: env.catalogStore, failures: 1}
	env.service.Stock = flaky

	event := payments.Event{ID: "evt_expired_flaky", Type: "checkout.session.expired", OrderID: order.ID}
	if err := env.service.ApplyWebhookEvent(context.Background(), event); !errors.Is(err, ErrStockReleaseFailed) {
		t.Fatalf("ApplyWebhookEvent error = %v, want %v so the webhook 500s and Stripe retries", err, ErrStockReleaseFailed)
	}
	failed := env.mustGetOrder(t, order.ID)
	if failed.Status != commerce.OrderStatusExpired {
		t.Fatalf("order status = %q, want %q (transition committed before the release failed)", failed.Status, commerce.OrderStatusExpired)
	}
	if !failed.StockReleasedAt.IsZero() {
		t.Errorf("StockReleasedAt = %v, want the claim returned after the failed release", failed.StockReleasedAt)
	}
	env.assertStock(t, 3, 3)

	// The Stripe redelivery re-enters the terminal-status replay path, which
	// must re-attempt the unreleased claim instead of acknowledging.
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("redelivered ApplyWebhookEvent returned error: %v", err)
	}
	env.assertStock(t, 5, 4)
	if env.mustGetOrder(t, order.ID).StockReleasedAt.IsZero() {
		t.Errorf("StockReleasedAt is zero after the successful release, want the claim recorded")
	}

	// Further redeliveries must not release a second time.
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("third ApplyWebhookEvent returned error: %v", err)
	}
	env.assertStock(t, 5, 4)
	if flaky.calls != 2 {
		t.Errorf("AdjustStock release calls = %d, want exactly 2 (one failure, one success)", flaky.calls)
	}
}

func TestPlaceOrderReentryRecoversFailedReleaseOfCanceledPointerOrder(t *testing.T) {
	env := newTestEnv(t)
	_, first := env.mustPlaceOrder(t, checkoutTestLines())
	flaky := &flakyStockStore{StockStore: env.catalogStore, failures: 1}
	env.service.Stock = flaky

	// The cancel commits the transition but the release fails transiently:
	// the order is canceled with its reservation still held.
	err := env.service.CancelPendingOrder(context.Background(), env.mustGetOrder(t, first.ID))
	if !errors.Is(err, ErrStockReleaseFailed) {
		t.Fatalf("CancelPendingOrder error = %v, want %v", err, ErrStockReleaseFailed)
	}
	if got := env.mustGetOrder(t, first.ID).Status; got != commerce.OrderStatusCanceled {
		t.Fatalf("order status = %q, want canceled", got)
	}
	env.assertStock(t, 3, 3)

	// The shopper places again: the stale-pointer cleanup must recover the
	// unreleased claim before reserving for the fresh order.
	_, second, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, checkoutTestLines()))
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected a fresh order, got the canceled one")
	}
	// First order's reservation released, second order's reservation held.
	env.assertStock(t, 3, 3)
	recovered := env.mustGetOrder(t, first.ID)
	if recovered.StockReleasedAt.IsZero() {
		t.Errorf("StockReleasedAt is zero, want the recovered release claim recorded")
	}

	// Replaying the cleanup must not release a second time.
	if err := env.service.CancelPendingOrder(context.Background(), recovered); err != nil {
		t.Fatalf("replayed CancelPendingOrder returned error: %v", err)
	}
	env.assertStock(t, 3, 3)
}

func TestCancelPendingOrderLostSameTargetRaceDoesNotDoubleRelease(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	stale := env.mustGetOrder(t, order.ID)

	if err := env.service.CancelPendingOrder(context.Background(), stale); err != nil {
		t.Fatalf("CancelPendingOrder returned error: %v", err)
	}
	env.assertStock(t, 5, 4)

	// A concurrent canceler that read the order while it was still pending:
	// its same-target transition no-ops as success, and the release claim is
	// what keeps it from releasing the same lines again.
	if err := env.service.CancelPendingOrder(context.Background(), stale); err != nil {
		t.Fatalf("racing CancelPendingOrder returned error: %v", err)
	}
	env.assertStock(t, 5, 4)
}

func TestPlaceOrderCartPointerRaceAdoptsExistingPendingOrder(t *testing.T) {
	env := newTestEnv(t)
	_, first := env.mustPlaceOrder(t, checkoutTestLines())

	// Simulate a request that read the cart before the first place-order won
	// the pointer: stale version, no pending pointer.
	input := env.placeOrderInput(t, checkoutTestLines())
	input.Cart = commerce.CartRecord{CustomerID: env.customer.ID}

	redirectURL, adopted, err := env.service.PlaceOrder(context.Background(), input)
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}
	wantURL := "/checkout/fake-pay?session_id=cs_fake_" + first.ID
	if redirectURL != wantURL {
		t.Errorf("redirect URL = %q, want the existing pointer's session %q", redirectURL, wantURL)
	}
	if adopted.ID != first.ID {
		t.Errorf("adopted order id = %q, want the pointer winner %q", adopted.ID, first.ID)
	}
	if record := env.currentCart(t); record.PendingOrderID != first.ID {
		t.Errorf("cart pending order id = %q, want %q left in place (no overwrite)", record.PendingOrderID, first.ID)
	}

	// This request's fresh order was canceled, its stock released, and its
	// session expired.
	orders, listErr := env.commerceStore.ListOrders(context.Background(), 0)
	if listErr != nil {
		t.Fatalf("ListOrders returned error: %v", listErr)
	}
	if len(orders) != 2 {
		t.Fatalf("order count = %d, want 2", len(orders))
	}
	var fresh commerce.Order
	for _, candidate := range orders {
		if candidate.ID != first.ID {
			fresh = candidate
		}
	}
	if fresh.Status != commerce.OrderStatusCanceled {
		t.Errorf("fresh order status = %q, want %q", fresh.Status, commerce.OrderStatusCanceled)
	}
	env.assertStock(t, 3, 3)
	session, err := env.provider.GetSession(context.Background(), "cs_fake_"+fresh.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.Status != payments.SessionStatusExpired {
		t.Errorf("fresh order session status = %q, want %q", session.Status, payments.SessionStatusExpired)
	}
}

func TestFingerprint(t *testing.T) {
	service := &Service{}
	base := service.Fingerprint(checkoutTestLines(), "addr-1")

	t.Run("stable under line reordering", func(t *testing.T) {
		lines := checkoutTestLines()
		lines[0], lines[1] = lines[1], lines[0]
		if got := service.Fingerprint(lines, "addr-1"); got != base {
			t.Errorf("reordered fingerprint = %q, want %q", got, base)
		}
	})

	t.Run("ignores cosmetic fields", func(t *testing.T) {
		lines := checkoutTestLines()
		lines[0].Name = "Renamed Bowl"
		lines[0].ImageURL = "/images/other.jpg"
		if got := service.Fingerprint(lines, "addr-1"); got != base {
			t.Errorf("cosmetic-change fingerprint = %q, want %q", got, base)
		}
	})

	changes := []struct {
		name   string
		mutate func(lines []commerce.OrderLine) ([]commerce.OrderLine, string)
	}{
		{
			name: "quantity change",
			mutate: func(lines []commerce.OrderLine) ([]commerce.OrderLine, string) {
				lines[0].Quantity = 3
				return lines, "addr-1"
			},
		},
		{
			name: "price change",
			mutate: func(lines []commerce.OrderLine) ([]commerce.OrderLine, string) {
				lines[0].UnitPriceCents = 1999
				return lines, "addr-1"
			},
		},
		{
			name: "variant change",
			mutate: func(lines []commerce.OrderLine) ([]commerce.OrderLine, string) {
				lines[1].VariantID = "var_s"
				return lines, "addr-1"
			},
		},
		{
			name: "address change",
			mutate: func(lines []commerce.OrderLine) ([]commerce.OrderLine, string) {
				return lines, "addr-2"
			},
		},
		{
			name: "line removed",
			mutate: func(lines []commerce.OrderLine) ([]commerce.OrderLine, string) {
				return lines[:1], "addr-1"
			},
		},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			lines, addressID := change.mutate(checkoutTestLines())
			if got := service.Fingerprint(lines, addressID); got == base {
				t.Errorf("fingerprint unchanged after %s", change.name)
			}
		})
	}

	t.Run("64 lowercase hex chars", func(t *testing.T) {
		if len(base) != 64 || strings.ToLower(base) != base {
			t.Errorf("fingerprint = %q, want 64 lowercase hex characters", base)
		}
	})
}
