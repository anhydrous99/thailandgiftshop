package checkout

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
)

var checkoutTestNow = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

// checkoutTestPasswordHash is an opaque fixture; nothing in this package
// verifies passwords (bcrypt cost 4 shape, never a real credential).
const checkoutTestPasswordHash = "$2a$04$checkout-test-fixture-hash"

const checkoutTestBaseURL = "https://shop.example.test"

func assertNoCheckoutCredentialLeak(t *testing.T, got string, sensitiveValues ...string) {
	t.Helper()
	for _, value := range sensitiveValues {
		if value != "" && strings.Contains(got, value) {
			t.Fatalf("output leaked sensitive value %q in %q", value, got)
		}
	}
	for _, marker := range []string{"cs_fake_", "session_id", "cancel_token", "access="} {
		if strings.Contains(got, marker) {
			t.Fatalf("output leaked sensitive marker %q in %q", marker, got)
		}
	}
}

func captureCheckoutLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := checkoutLogger
	checkoutLogger = slog.New(slog.NewJSONHandler(&buffer, nil))
	t.Cleanup(func() {
		checkoutLogger = previous
	})
	return &buffer
}

func checkoutTestGuestAccessURL(order commerce.Order, now time.Time) string {
	_ = now
	return checkoutTestBaseURL + "/orders/" + order.ID + "?access=test-access"
}

func TestGuestOrderAccessURLMintsValidToken(t *testing.T) {
	order := commerce.Order{ID: "ord_guest_access_token", PaidAt: checkoutTestNow.Add(-time.Hour)}
	secret := "customer-session-secret-with-enough-entropy"

	accessURL, err := GuestOrderAccessURL(order, checkoutTestNow, checkoutTestBaseURL+"/", secret)
	if err != nil {
		t.Fatalf("GuestOrderAccessURL returned error: %v", err)
	}
	parsed, err := url.Parse(accessURL)
	if err != nil {
		t.Fatalf("access URL does not parse: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "shop.example.test" || parsed.Path != "/orders/"+order.ID {
		t.Fatalf("access URL = %q, want shop order detail URL", accessURL)
	}
	token := parsed.Query().Get(GuestOrderAccessParam)
	if token == "" {
		t.Fatalf("access URL = %q, want %s query token", accessURL, GuestOrderAccessParam)
	}
	if !ValidGuestOrderAccessToken(token, order.ID, secret, checkoutTestNow) {
		t.Fatal("ValidGuestOrderAccessToken matching token = false, want true")
	}
	if ValidGuestOrderAccessToken(token, "other-order", secret, checkoutTestNow) {
		t.Fatal("ValidGuestOrderAccessToken wrong order = true, want false")
	}
	if ValidGuestOrderAccessToken(token, order.ID, secret, order.PaidAt.Add(GuestOrderAccessTTL+time.Second)) {
		t.Fatal("ValidGuestOrderAccessToken expired token = true, want false")
	}
}

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
	createRefundErr          error
	createRefundFn           func(ctx context.Context, input payments.RefundInput) (payments.Refund, error)
	createRefundCalls        int
	getRefundErr             error
	getRefundCalls           int
}

func (p *stubProvider) CreateRefund(ctx context.Context, input payments.RefundInput) (payments.Refund, error) {
	p.createRefundCalls++
	if p.createRefundErr != nil {
		return payments.Refund{}, p.createRefundErr
	}
	if p.createRefundFn != nil {
		return p.createRefundFn(ctx, input)
	}
	return p.FakeProvider.CreateRefund(ctx, input)
}

func (p *stubProvider) GetRefund(ctx context.Context, refundID string) (payments.Refund, error) {
	p.getRefundCalls++
	if p.getRefundErr != nil {
		return payments.Refund{}, p.getRefundErr
	}
	return p.FakeProvider.GetRefund(ctx, refundID)
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
// call counters for the webhook dedupe assertions. The cart counters let the
// guest tests assert the service never touches a CART row for guest orders.
type hookStore struct {
	commerce.Store
	getOrderErr                 error
	markStripeEventCalls        int
	reserveEmailEventCalls      int
	markEmailEventSentCalls     int
	markEmailFailedEvents       []recordedEmailFailure
	getCartCalls                int
	putCartCalls                int
	transitionConflictOnce      bool
	transitionConflictFired     bool
	transitionConflictAdvanceTo commerce.OrderStatus
}

type recordedEmailFailure struct {
	orderID string
	key     string
	reason  string
	at      time.Time
}

func (h *hookStore) GetCart(ctx context.Context, customerID string) (commerce.CartRecord, bool, error) {
	h.getCartCalls++
	return h.Store.GetCart(ctx, customerID)
}

func (h *hookStore) PutCart(ctx context.Context, record commerce.CartRecord) (commerce.CartRecord, error) {
	h.putCartCalls++
	return h.Store.PutCart(ctx, record)
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

func (h *hookStore) ReserveEmailEvent(ctx context.Context, event commerce.EmailEvent) (bool, error) {
	h.reserveEmailEventCalls++
	return h.Store.ReserveEmailEvent(ctx, event)
}

func (h *hookStore) MarkEmailEventSent(ctx context.Context, orderID string, key string, sentAt time.Time) error {
	h.markEmailEventSentCalls++
	return h.Store.MarkEmailEventSent(ctx, orderID, key, sentAt)
}

func (h *hookStore) MarkEmailEventFailed(ctx context.Context, orderID string, key string, failedAt time.Time, reason string) error {
	h.markEmailFailedEvents = append(h.markEmailFailedEvents, recordedEmailFailure{orderID: orderID, key: key, reason: reason, at: failedAt})
	return h.Store.MarkEmailEventFailed(ctx, orderID, key, failedAt, reason)
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
		if h.transitionConflictAdvanceTo != "" {
			advancePatch := commerce.OrderPatch{Actor: commerce.OrderActorAdmin}
			if _, err := h.Store.TransitionOrder(ctx, orderID, to, h.transitionConflictAdvanceTo, advancePatch); err != nil {
				return commerce.Order{}, err
			}
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
	emailSender   *email.FakeSender
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
	emailSender := email.NewFakeSenderWithClock(func() time.Time { return checkoutTestNow })

	customer, err := commerceStore.CreateCustomer(context.Background(), "Shopper@example.test", "shopper@example.test", checkoutTestPasswordHash)
	if err != nil {
		t.Fatalf("CreateCustomer returned error: %v", err)
	}

	return &testEnv{
		service: &Service{
			Commerce:            hooks,
			Payments:            provider,
			Stock:               catalogStore,
			Metrics:             metrics,
			EmailSender:         emailSender,
			BaseURL:             checkoutTestBaseURL + "/",
			Now:                 func() time.Time { return checkoutTestNow },
			GuestOrderAccessURL: checkoutTestGuestAccessURL,
		},
		commerceStore: commerceStore,
		hooks:         hooks,
		catalogStore:  catalogStore,
		provider:      provider,
		metrics:       metrics,
		emailSender:   emailSender,
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
	page, err := env.commerceStore.ListOrders(context.Background(), 0, commerce.OrderCursor{})
	if err != nil {
		t.Fatalf("ListOrders returned error: %v", err)
	}
	return len(page.Orders)
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

// mustPaidOrder places an order with the standard test lines and finalizes
// its payment, returning the paid order (stock 3/3 reserved).
func (env *testEnv) mustPaidOrder(t *testing.T) commerce.Order {
	t.Helper()
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	paid, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}
	return paid
}

type failingEmailSender struct {
	err   error
	calls int
}

func (s *failingEmailSender) Kind() string { return "failing" }

func (s *failingEmailSender) Send(ctx context.Context, message email.Message) (email.Message, error) {
	_ = ctx
	s.calls++
	return email.Message{}, s.err
}

func assertEmailMessages(t *testing.T, sender *email.FakeSender, want []struct {
	kind string
	key  string
	to   string
}) {
	t.Helper()
	messages := sender.Messages()
	if len(messages) != len(want) {
		t.Fatalf("email messages = %d (%#v), want %d", len(messages), messages, len(want))
	}
	for i, message := range messages {
		if message.Kind != want[i].kind || message.EventKey != want[i].key || message.To != want[i].to {
			t.Fatalf("message[%d] = kind %q key %q to %q, want %q %q %q", i, message.Kind, message.EventKey, message.To, want[i].kind, want[i].key, want[i].to)
		}
	}
}

func TestFinalizePaymentSendsOrderPlacedEmailOnce(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)

	paid, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}
	placedKey := fmt.Sprintf("order:%s:placed:v%d", paid.ID, paid.Version)
	assertEmailMessages(t, env.emailSender, []struct {
		kind string
		key  string
		to   string
	}{{email.MessageKindOrderPlaced, placedKey, order.Email}})

	if _, err := env.service.FinalizePayment(context.Background(), order.ID, session); err != nil {
		t.Fatalf("replayed FinalizePayment returned error: %v", err)
	}
	event := payments.Event{ID: "evt_completed_email_replay", Type: "checkout.session.completed", SessionID: session.ID, OrderID: order.ID, Session: session}
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent replay returned error: %v", err)
	}
	assertEmailMessages(t, env.emailSender, []struct {
		kind string
		key  string
		to   string
	}{{email.MessageKindOrderPlaced, placedKey, order.Email}})
	if env.hooks.markEmailEventSentCalls != 1 {
		t.Fatalf("MarkEmailEventSent calls = %d, want 1", env.hooks.markEmailEventSentCalls)
	}
}

func TestCustomerLifecycleEmailsUseOrderDetailURL(t *testing.T) {
	env := newTestEnv(t)
	paid := env.mustPaidOrder(t)
	message := env.emailSender.Messages()[0]

	wantURL := checkoutTestBaseURL + "/orders/" + paid.ID
	if !strings.Contains(message.Text, wantURL) || !strings.Contains(message.HTML, wantURL) {
		t.Fatalf("customer placed email missing order detail URL")
	}
	if strings.Contains(message.Text, confirmURLPath) || strings.Contains(message.HTML, confirmURLPath) {
		t.Fatalf("customer placed email used confirm URL instead of order detail URL")
	}
}

func TestGuestLifecycleEmailsUseOrderAccessURL(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	paid, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}

	shipped := paid
	shipped.Status = commerce.OrderStatusShipped
	shipped.Version++
	shipped.TrackingCarrier = "Thailand Post"
	shipped.TrackingNumber = "TH1234567890"
	env.service.NotifyTrackingUpdated(context.Background(), shipped)
	delivered := shipped
	delivered.Status = commerce.OrderStatusDelivered
	delivered.Version++
	env.service.NotifyOrderStatusChanged(context.Background(), delivered, commerce.OrderStatusShipped, commerce.OrderStatusDelivered)

	messages := env.emailSender.Messages()
	if len(messages) != 3 {
		t.Fatalf("guest lifecycle message count = %d, want 3", len(messages))
	}
	accessURL := checkoutTestGuestAccessURL(paid, checkoutTestNow)
	for _, message := range messages {
		if !strings.Contains(message.Text, accessURL) || !strings.Contains(message.HTML, accessURL) {
			t.Fatalf("guest %s email missing tokenized order access URL", message.Kind)
		}
		if strings.Contains(message.Text, confirmURLPath) || strings.Contains(message.HTML, confirmURLPath) {
			t.Fatalf("guest %s email contains confirm URL", message.Kind)
		}
	}
}

func TestGuestLifecycleEmailsDoNotFallbackToConfirmURL(t *testing.T) {
	tests := []struct {
		name   string
		config func(*Service)
	}{
		{name: "missing access URL builder", config: func(service *Service) { service.GuestOrderAccessURL = nil }},
		{name: "empty access URL", config: func(service *Service) {
			service.GuestOrderAccessURL = func(commerce.Order, time.Time) string { return "" }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t)
			test.config(env.service)
			_, order := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)
			session := env.paidSession(t, order.StripeCheckoutSessionID)
			if _, err := env.service.FinalizePayment(context.Background(), order.ID, session); err != nil {
				t.Fatalf("FinalizePayment returned error: %v", err)
			}

			messages := env.emailSender.Messages()
			if len(messages) != 1 {
				t.Fatalf("guest lifecycle message count = %d, want 1", len(messages))
			}
			message := messages[0]
			if strings.Contains(message.Text, confirmURLPath) || strings.Contains(message.HTML, confirmURLPath) {
				t.Fatalf("guest placed email contains confirm URL")
			}
			if strings.Contains(message.Text, "/orders/"+order.ID) || strings.Contains(message.HTML, "/orders/"+order.ID) {
				t.Fatalf("guest placed email contains tokenless order detail URL")
			}
		})
	}
}

func TestGuestTerminalUnpaidEmailsUseOrderAccessURL(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)
	event := payments.Event{ID: "evt_guest_failed_email", Type: "checkout.session.async_payment_failed", OrderID: order.ID}

	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	failed := env.mustGetOrder(t, order.ID)
	messages := env.emailSender.Messages()
	if len(messages) != 1 {
		t.Fatalf("guest terminal email count = %d, want 1", len(messages))
	}
	accessURL := checkoutTestGuestAccessURL(failed, checkoutTestNow)
	message := messages[0]
	if !strings.Contains(message.Text, accessURL) || !strings.Contains(message.HTML, accessURL) {
		t.Fatalf("guest terminal email missing tokenized order access URL")
	}
	if strings.Contains(message.Text, confirmURLPath) || strings.Contains(message.HTML, confirmURLPath) {
		t.Fatalf("guest terminal email contains confirm URL")
	}
}

func TestFinalizePaymentLostRaceToShippedDoesNotSendLaterPlacedEmail(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	paidPlacedKey := fmt.Sprintf("order:%s:placed:v%d", order.ID, order.Version+1)
	reserved, err := env.commerceStore.ReserveEmailEvent(context.Background(), commerce.EmailEvent{
		Key:          paidPlacedKey,
		Kind:         email.MessageKindOrderPlaced,
		OrderID:      order.ID,
		OrderVersion: order.Version + 1,
		To:           order.Email,
		Status:       "reserved",
		Attempts:     1,
		CreatedAt:    checkoutTestNow,
	})
	if err != nil || !reserved {
		t.Fatalf("ReserveEmailEvent for prior placed email = %t %v, want reserved", reserved, err)
	}
	if err := env.commerceStore.MarkEmailEventSent(context.Background(), order.ID, paidPlacedKey, checkoutTestNow); err != nil {
		t.Fatalf("MarkEmailEventSent for prior placed email returned error: %v", err)
	}

	env.hooks.transitionConflictOnce = true
	env.hooks.transitionConflictAdvanceTo = commerce.OrderStatusShipped

	current, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}
	if current.Status != commerce.OrderStatusShipped {
		t.Fatalf("returned status = %q, want shipped winner adopted", current.Status)
	}
	if messages := env.emailSender.Messages(); len(messages) != 0 {
		t.Fatalf("messages after shipped conflict adoption = %#v, want no later-version placed email", messages)
	}
	if env.hooks.reserveEmailEventCalls != 0 || env.hooks.markEmailEventSentCalls != 0 {
		t.Fatalf("email event calls = reserve %d sent %d, want no checkout email attempts on shipped adoption", env.hooks.reserveEmailEventCalls, env.hooks.markEmailEventSentCalls)
	}
}

func TestCheckoutLifecycleStatusEmails(t *testing.T) {
	t.Run("pending payment failure and expiry transitions", func(t *testing.T) {
		for _, target := range []struct {
			name      string
			eventType string
			status    commerce.OrderStatus
		}{
			{name: "payment failed", eventType: "checkout.session.async_payment_failed", status: commerce.OrderStatusPaymentFailed},
			{name: "expired", eventType: "checkout.session.expired", status: commerce.OrderStatusExpired},
		} {
			t.Run(target.name, func(t *testing.T) {
				env := newTestEnv(t)
				_, order := env.mustPlaceOrder(t, checkoutTestLines())
				event := payments.Event{ID: "evt_email_" + string(target.status), Type: target.eventType, OrderID: order.ID}
				if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
					t.Fatalf("ApplyWebhookEvent returned error: %v", err)
				}
				_ = env.mustGetOrder(t, order.ID)
				key := fmt.Sprintf("order:%s:status:%s:%s:v%d", order.ID, commerce.OrderStatusPendingPayment, target.status, order.Version+1)
				assertEmailMessages(t, env.emailSender, []struct {
					kind string
					key  string
					to   string
				}{{email.MessageKindOrderStatusChange, key, order.Email}})
			})
		}
	})

	t.Run("cancel transition", func(t *testing.T) {
		env := newTestEnv(t)
		_, order := env.mustPlaceOrder(t, checkoutTestLines())
		if err := env.service.CancelPendingOrderAs(context.Background(), env.mustGetOrder(t, order.ID), commerce.OrderActorAdmin); err != nil {
			t.Fatalf("CancelPendingOrderAs returned error: %v", err)
		}
		_ = env.mustGetOrder(t, order.ID)
		key := fmt.Sprintf("order:%s:status:%s:%s:v%d", order.ID, commerce.OrderStatusPendingPayment, commerce.OrderStatusCanceled, order.Version+1)
		assertEmailMessages(t, env.emailSender, []struct {
			kind string
			key  string
			to   string
		}{{email.MessageKindOrderStatusChange, key, order.Email}})
	})

	t.Run("refund issue and settlement transitions", func(t *testing.T) {
		env := newTestEnv(t)
		order := env.mustPaidOrder(t)
		env.emailSender.Clear()
		updated, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
		if err != nil {
			t.Fatalf("RefundOrderAs returned error: %v", err)
		}
		pendingKey := fmt.Sprintf("order:%s:status:%s:%s:v%d", order.ID, commerce.OrderStatusPaid, commerce.OrderStatusRefundPending, order.Version+1)
		refundedKey := fmt.Sprintf("order:%s:status:%s:%s:v%d", order.ID, commerce.OrderStatusRefundPending, commerce.OrderStatusRefunded, updated.Version)
		assertEmailMessages(t, env.emailSender, []struct {
			kind string
			key  string
			to   string
		}{
			{email.MessageKindOrderStatusChange, pendingKey, order.Email},
			{email.MessageKindOrderStatusChange, refundedKey, order.Email},
		})
	})

	t.Run("refund failure and retry transitions", func(t *testing.T) {
		env := newTestEnv(t)
		order := env.mustPaidOrder(t)
		env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
		pending, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
		if err != nil {
			t.Fatalf("RefundOrderAs returned error: %v", err)
		}
		env.emailSender.Clear()
		failedEvent := refundWebhookEvent("evt_email_refund_failed", "refund.failed", payments.Refund{
			ID:              pending.StripeRefundID,
			PaymentIntentID: order.StripePaymentIntentID,
			OrderID:         order.ID,
			Attempt:         pending.RefundAttempt,
			Status:          payments.RefundStatusFailed,
			FailureReason:   "expired_or_canceled_card",
		})
		if err := env.service.ApplyWebhookEvent(context.Background(), failedEvent); err != nil {
			t.Fatalf("ApplyWebhookEvent(failed) returned error: %v", err)
		}
		failed := env.mustGetOrder(t, order.ID)
		failedKey := fmt.Sprintf("order:%s:status:%s:%s:v%d", order.ID, commerce.OrderStatusRefundPending, commerce.OrderStatusRefundFailed, failed.Version)
		assertEmailMessages(t, env.emailSender, []struct {
			kind string
			key  string
			to   string
		}{{email.MessageKindOrderStatusChange, failedKey, order.Email}})

		env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
		env.emailSender.Clear()
		retried, err := env.service.RefundOrderAs(context.Background(), failed, commerce.OrderActorAdmin)
		if err != nil {
			t.Fatalf("retry RefundOrderAs returned error: %v", err)
		}
		retryKey := fmt.Sprintf("order:%s:status:%s:%s:v%d", order.ID, commerce.OrderStatusRefundFailed, commerce.OrderStatusRefundPending, retried.Version)
		assertEmailMessages(t, env.emailSender, []struct {
			kind string
			key  string
			to   string
		}{{email.MessageKindOrderStatusChange, retryKey, order.Email}})
	})
}

func TestOrderEmailFailureDoesNotRollbackState(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	failure := &failingEmailSender{err: errors.New("ses rejected recipient")}
	env.service.EmailSender = failure

	paid, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment returned error despite email failure: %v", err)
	}
	if paid.Status != commerce.OrderStatusPaid {
		t.Fatalf("returned order status = %q, want paid", paid.Status)
	}
	stored := env.mustGetOrder(t, order.ID)
	if stored.Status != commerce.OrderStatusPaid || stored.StripePaymentIntentID == "" {
		t.Fatalf("stored order = %s payment intent %q, want paid state committed", stored.Status, stored.StripePaymentIntentID)
	}
	if failure.calls != 1 {
		t.Fatalf("failing sender calls = %d, want 1", failure.calls)
	}
	if len(env.hooks.markEmailFailedEvents) != 1 {
		t.Fatalf("failed email events = %#v, want one", env.hooks.markEmailFailedEvents)
	}
	failed := env.hooks.markEmailFailedEvents[0]
	wantKey := fmt.Sprintf("order:%s:placed:v%d", order.ID, paid.Version)
	if failed.orderID != order.ID || failed.key != wantKey || !strings.Contains(failed.reason, "ses rejected recipient") || !failed.at.Equal(checkoutTestNow) {
		t.Fatalf("failed event = %#v, want order %s key %s sanitized failure metadata", failed, order.ID, wantKey)
	}
	if messages := env.emailSender.Messages(); len(messages) != 0 {
		t.Fatalf("fake sender messages = %#v, want none after replacing sender with failing sender", messages)
	}
	if got := env.metrics.count(observability.MetricOrderEmail, outcomeOrderEmailSendError); got != 1 {
		t.Fatalf("order email send-error metrics = %d, want 1", got)
	}
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

	redirectURL, second, err := env.service.PlaceOrder(context.Background(), env.placeOrderInput(t, checkoutTestLines()))
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-entry order id = %q, want the paid order %q", second.ID, first.ID)
	}
	if wantURL := checkoutTestBaseURL + "/orders/" + first.ID; redirectURL != wantURL {
		t.Fatalf("re-entry URL = %q, want %q", redirectURL, wantURL)
	}
	if count := env.orderCount(t); count != 1 {
		t.Fatalf("order count = %d, want 1 (no duplicate order)", count)
	}
	if got := env.mustGetOrder(t, first.ID).Status; got != commerce.OrderStatusPaid {
		t.Errorf("paid order status = %q, want it untouched as %q", got, commerce.OrderStatusPaid)
	}
	record := env.currentCart(t)
	if len(record.Lines) != 0 || record.PendingOrderID != "" || record.PendingFingerprint != "" {
		t.Fatalf("cart after paid re-entry = %+v, want paid lines and pointer cleared", record)
	}
	env.assertStock(t, 3, 3)
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
			page, listErr := env.commerceStore.ListOrders(context.Background(), 0, commerce.OrderCursor{})
			if listErr != nil {
				t.Fatalf("ListOrders returned error: %v", listErr)
			}
			orders := page.Orders
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

func TestFinalizePaymentPreservesCartLinesAddedDuringPayment(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	record := env.currentCart(t)
	record.Lines = []cart.Line{
		{Slug: "carved-coconut-bowl", Quantity: 3},
		{Slug: "handwoven-indigo-scarf", VariantID: "var_m", Quantity: 1},
		{Slug: "handwoven-indigo-scarf", VariantID: "var_s", Quantity: 1},
	}
	if _, err := env.commerceStore.PutCart(context.Background(), record); err != nil {
		t.Fatalf("PutCart returned error: %v", err)
	}

	session := env.paidSession(t, order.StripeCheckoutSessionID)
	if _, err := env.service.FinalizePayment(context.Background(), order.ID, session); err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}

	record = env.currentCart(t)
	want := []cart.Line{
		{Slug: "carved-coconut-bowl", Quantity: 1},
		{Slug: "handwoven-indigo-scarf", VariantID: "var_s", Quantity: 1},
	}
	if fmt.Sprint(record.Lines) != fmt.Sprint(want) || record.PendingOrderID != "" || record.PendingFingerprint != "" {
		t.Fatalf("cart after finalize = %+v, want remaining newly-added lines %#v and cleared pointer", record, want)
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
		if messages := env.emailSender.Messages(); len(messages) != 0 {
			t.Fatalf("system cancel messages = %#v, want none", messages)
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
	assertNoCheckoutCredentialLeak(t, err.Error(), order.StripeCheckoutSessionID)
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

func TestFinalizePaymentErrorsDoNotExposePaymentSessionCredentials(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())

	_, mismatchErr := env.service.FinalizePayment(context.Background(), order.ID, payments.Session{
		ID:            order.StripeCheckoutSessionID,
		OrderID:       "ord_other",
		PaymentStatus: paymentStatusPaid,
	})
	if mismatchErr == nil {
		t.Fatal("FinalizePayment mismatch error = nil, want error")
	}
	assertNoCheckoutCredentialLeak(t, mismatchErr.Error(), order.StripeCheckoutSessionID)

	_, unpaidErr := env.service.FinalizePayment(context.Background(), order.ID, payments.Session{
		ID:            order.StripeCheckoutSessionID,
		OrderID:       order.ID,
		PaymentStatus: paymentStatusUnpaid,
	})
	if unpaidErr == nil {
		t.Fatal("FinalizePayment unpaid error = nil, want error")
	}
	assertNoCheckoutCredentialLeak(t, unpaidErr.Error(), order.StripeCheckoutSessionID)
}

func TestFinalizePaymentCardExpansionLogRedactsPaymentSessionCredentials(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	env.provider.getSessionErr = fmt.Errorf("stripe lookup failed for %s", order.StripeCheckoutSessionID)
	logs := captureCheckoutLogs(t)

	_, err := env.service.FinalizePayment(context.Background(), order.ID, payments.Session{
		ID:            order.StripeCheckoutSessionID,
		OrderID:       order.ID,
		PaymentStatus: paymentStatusPaid,
	})
	if err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}
	output := logs.String()
	if !strings.Contains(output, "checkout could not expand the payment session card details") {
		t.Fatalf("checkout log output = %q, want card expansion warning", output)
	}
	assertNoCheckoutCredentialLeak(t, output, order.StripeCheckoutSessionID)
}

func TestApplyWebhookEventPaidAfterTerminalIssuesAutoRefund(t *testing.T) {
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
	refunded := env.mustGetOrder(t, order.ID)
	if refunded.Status != commerce.OrderStatusCanceled {
		t.Errorf("order status = %q, want it left %q", refunded.Status, commerce.OrderStatusCanceled)
	}
	if refunded.StripeRefundID != "re_fake_"+order.ID+"_1" || refunded.RefundAttempt != 1 {
		t.Errorf("audit marker = %q attempt %d, want the auto-refund recorded", refunded.StripeRefundID, refunded.RefundAttempt)
	}
	if env.provider.createRefundCalls != 1 {
		t.Errorf("CreateRefund calls = %d, want 1", env.provider.createRefundCalls)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "paid_after_terminal"); got != 1 {
		t.Errorf("paid_after_terminal metric count = %d, want 1", got)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "error"); got != 0 {
		t.Errorf("error metric count = %d, want 0 (this is an ops condition, not a transient failure)", got)
	}
	// System-initiated refunds ride the distinct issued_auto outcome.
	if got := env.metrics.count(observability.MetricCheckoutRefund, "issued_auto"); got != 1 {
		t.Errorf("issued_auto metric count = %d, want 1", got)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "issued"); got != 0 {
		t.Errorf("issued metric count = %d, want 0 for the auto path", got)
	}

	// A redelivery short-circuits on the marker: no second provider refund.
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("redelivered ApplyWebhookEvent returned error: %v", err)
	}
	if env.provider.createRefundCalls != 1 {
		t.Errorf("CreateRefund calls after redelivery = %d, want still 1", env.provider.createRefundCalls)
	}
}

func TestApplyWebhookEventPaidAfterTerminalRefundFailureIsRetryable(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	session := env.paidSession(t, order.StripeCheckoutSessionID)
	if _, err := env.commerceStore.TransitionOrder(context.Background(), order.ID, commerce.OrderStatusPendingPayment, commerce.OrderStatusCanceled, commerce.OrderPatch{Actor: commerce.OrderActorAdmin}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}
	env.provider.createRefundErr = errors.New("stripe api down")

	event := payments.Event{ID: "evt_terminal_retry", Type: "checkout.session.completed", SessionID: session.ID, OrderID: order.ID, Session: session}
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err == nil {
		t.Fatal("ApplyWebhookEvent returned nil, want a retryable error so Stripe redelivers")
	}
	if got := env.mustGetOrder(t, order.ID).StripeRefundID; got != "" {
		t.Errorf("StripeRefundID = %q, want empty after the failed create", got)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "provider_error"); got != 1 {
		t.Errorf("provider_error metric count = %d, want 1", got)
	}

	// The redelivery succeeds once the provider recovers.
	env.provider.createRefundErr = nil
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("redelivered ApplyWebhookEvent returned error: %v", err)
	}
	if got := env.mustGetOrder(t, order.ID).StripeRefundID; got == "" {
		t.Errorf("StripeRefundID empty after recovery, want the auto-refund marker")
	}
}

func TestApplyWebhookEventPaidAfterTerminalWithoutPaymentIntentFallsBackToManualLog(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	env.paidSession(t, order.StripeCheckoutSessionID)
	if _, err := env.commerceStore.TransitionOrder(context.Background(), order.ID, commerce.OrderStatusPendingPayment, commerce.OrderStatusCanceled, commerce.OrderPatch{Actor: commerce.OrderActorAdmin}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}

	// The event session has no payment intent and is unknown to the provider,
	// so the GetSession expansion cannot fill it either: retries cannot
	// conjure a PI, so the webhook ACKs with the manual-refund log line.
	event := payments.Event{
		ID:      "evt_terminal_no_pi",
		Type:    "checkout.session.completed",
		OrderID: order.ID,
		Session: payments.Session{ID: "cs_unknown_session", OrderID: order.ID, PaymentStatus: "paid"},
	}
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent error = %v, want nil ACK", err)
	}
	if env.provider.createRefundCalls != 0 {
		t.Errorf("CreateRefund calls = %d, want 0 without a payment intent", env.provider.createRefundCalls)
	}
	if got := env.mustGetOrder(t, order.ID).StripeRefundID; got != "" {
		t.Errorf("StripeRefundID = %q, want empty", got)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "provider_error"); got != 1 {
		t.Errorf("provider_error metric count = %d, want 1 (the alarmed manual fallback)", got)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "issued_auto"); got != 0 {
		t.Errorf("issued_auto metric count = %d, want 0", got)
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
	page, listErr := env.commerceStore.ListOrders(context.Background(), 0, commerce.OrderCursor{})
	if listErr != nil {
		t.Fatalf("ListOrders returned error: %v", listErr)
	}
	orders := page.Orders
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

// --- Automated refund tests ---

// refundWebhookEvent builds a refund.* event the way eventFromStripeEvent
// would map it: OrderID lifted from the refund's metadata.
func refundWebhookEvent(eventID string, eventType string, refund payments.Refund) payments.Event {
	return payments.Event{ID: eventID, Type: eventType, OrderID: refund.OrderID, Refund: refund}
}

func TestRefundOrderAsFromPaidSettlesSynchronously(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	env.assertStock(t, 3, 3)

	updated, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if err != nil {
		t.Fatalf("RefundOrderAs returned error: %v", err)
	}
	if updated.Status != commerce.OrderStatusRefunded {
		t.Fatalf("order status = %q, want refunded", updated.Status)
	}
	stored := env.mustGetOrder(t, order.ID)
	if stored.StripeRefundID != "re_fake_"+order.ID+"_1" || stored.RefundAttempt != 1 {
		t.Errorf("refund fields = %q attempt %d, want re_fake_%s_1 attempt 1", stored.StripeRefundID, stored.RefundAttempt, order.ID)
	}
	if stored.RefundedAt.IsZero() {
		t.Errorf("RefundedAt is zero, want settlement timestamp")
	}
	wantHistory := []struct {
		status commerce.OrderStatus
		actor  string
	}{
		{commerce.OrderStatusPendingPayment, commerce.OrderActorCustomer},
		{commerce.OrderStatusPaid, commerce.OrderActorStripe},
		{commerce.OrderStatusRefundPending, commerce.OrderActorAdmin},
		{commerce.OrderStatusRefunded, commerce.OrderActorStripe},
	}
	if len(stored.StatusHistory) != len(wantHistory) {
		t.Fatalf("history length = %d, want %d: %#v", len(stored.StatusHistory), len(wantHistory), stored.StatusHistory)
	}
	for i, want := range wantHistory {
		if stored.StatusHistory[i].Status != want.status || stored.StatusHistory[i].Actor != want.actor {
			t.Errorf("history[%d] = %s/%s, want %s/%s", i, stored.StatusHistory[i].Status, stored.StatusHistory[i].Actor, want.status, want.actor)
		}
	}
	// Stock released exactly once through the claim protocol.
	env.assertStock(t, 5, 4)
	if stored.StockReleasedAt.IsZero() {
		t.Errorf("StockReleasedAt is zero, want the release claim recorded")
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "issued"); got != 1 {
		t.Errorf("issued metric count = %d, want 1", got)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "settled"); got != 1 {
		t.Errorf("settled metric count = %d, want 1", got)
	}
}

func TestRefundOrderAsFromShippedAndDeliveredKeepsStock(t *testing.T) {
	for _, target := range []commerce.OrderStatus{commerce.OrderStatusShipped, commerce.OrderStatusDelivered} {
		env := newTestEnv(t)
		order := env.mustPaidOrder(t)
		shippedAt := checkoutTestNow
		shipped, err := env.commerceStore.TransitionOrder(context.Background(), order.ID, commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderPatch{Actor: commerce.OrderActorAdmin, ShippedAt: &shippedAt})
		if err != nil {
			t.Fatalf("TransitionOrder to shipped returned error: %v", err)
		}
		current := shipped
		if target == commerce.OrderStatusDelivered {
			current, err = env.commerceStore.TransitionOrder(context.Background(), order.ID, commerce.OrderStatusShipped, commerce.OrderStatusDelivered, commerce.OrderPatch{Actor: commerce.OrderActorAdmin})
			if err != nil {
				t.Fatalf("TransitionOrder to delivered returned error: %v", err)
			}
		}
		counting := &flakyStockStore{StockStore: env.catalogStore}
		env.service.Stock = counting

		updated, err := env.service.RefundOrderAs(context.Background(), current, commerce.OrderActorAdmin)
		if err != nil {
			t.Fatalf("RefundOrderAs from %s returned error: %v", target, err)
		}
		if updated.Status != commerce.OrderStatusRefunded {
			t.Fatalf("order status = %q, want refunded", updated.Status)
		}
		// Shipped goods are never restocked: zero stock adjustments.
		if counting.calls != 0 {
			t.Errorf("AdjustStock calls = %d, want 0 for a %s refund", counting.calls, target)
		}
		env.assertStock(t, 3, 3)
		if !env.mustGetOrder(t, order.ID).StockReleasedAt.IsZero() {
			t.Errorf("StockReleasedAt set for a shipped refund, want zero")
		}
	}
}

func TestRefundOrderAsRejectsNonRefundableStatuses(t *testing.T) {
	env := newTestEnv(t)
	_, pending := env.mustPlaceOrder(t, checkoutTestLines())

	if _, err := env.service.RefundOrderAs(context.Background(), pending, commerce.OrderActorAdmin); !errors.Is(err, ErrOrderNotRefundable) {
		t.Fatalf("RefundOrderAs(pending) error = %v, want %v", err, ErrOrderNotRefundable)
	}
	if err := env.service.CancelPendingOrder(context.Background(), env.mustGetOrder(t, pending.ID)); err != nil {
		t.Fatalf("CancelPendingOrder returned error: %v", err)
	}
	canceled := env.mustGetOrder(t, pending.ID)
	if _, err := env.service.RefundOrderAs(context.Background(), canceled, commerce.OrderActorAdmin); !errors.Is(err, ErrOrderNotRefundable) {
		t.Fatalf("RefundOrderAs(canceled) error = %v, want %v", err, ErrOrderNotRefundable)
	}
	if got := env.mustGetOrder(t, pending.ID).Status; got != commerce.OrderStatusCanceled {
		t.Errorf("order status = %q, want canceled untouched", got)
	}
	if env.provider.createRefundCalls != 0 {
		t.Errorf("CreateRefund calls = %d, want 0", env.provider.createRefundCalls)
	}
}

func TestRefundOrderAsRequiresPaymentIntentAndProvider(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)

	empty := ""
	stripped, err := env.commerceStore.PatchOrder(context.Background(), order.ID, commerce.OrderStatusPaid, order.Version, commerce.OrderPatch{StripePaymentIntentID: &empty})
	if err != nil {
		t.Fatalf("PatchOrder returned error: %v", err)
	}
	if _, err := env.service.RefundOrderAs(context.Background(), stripped, commerce.OrderActorAdmin); !errors.Is(err, ErrRefundNotIssuable) {
		t.Fatalf("RefundOrderAs(no PI) error = %v, want %v", err, ErrRefundNotIssuable)
	}

	env.service.Payments = nil
	if _, err := env.service.RefundOrderAs(context.Background(), stripped, commerce.OrderActorAdmin); !errors.Is(err, ErrRefundProviderUnavailable) {
		t.Fatalf("RefundOrderAs(nil provider) error = %v, want %v", err, ErrRefundProviderUnavailable)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "provider_error"); got != 1 {
		t.Errorf("provider_error metric count = %d, want 1 (nil provider only)", got)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want paid untouched", got)
	}
}

func TestRefundOrderAsProviderFailureLeavesOrderUntouched(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	env.provider.createRefundErr = errors.New("stripe api down")

	_, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if err == nil || errors.Is(err, ErrOrderNotRefundable) {
		t.Fatalf("RefundOrderAs error = %v, want the provider error", err)
	}
	stored := env.mustGetOrder(t, order.ID)
	if stored.Status != commerce.OrderStatusPaid || stored.StripeRefundID != "" || stored.RefundAttempt != 0 {
		t.Errorf("order = %#v, want paid and untouched", stored)
	}
	env.assertStock(t, 3, 3)
	if got := env.metrics.count(observability.MetricCheckoutRefund, "provider_error"); got != 1 {
		t.Errorf("provider_error metric count = %d, want 1", got)
	}
}

func TestRefundOrderAsAlreadyRefundedSentinelPropagates(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	env.provider.createRefundErr = fmt.Errorf("%w: payment intent pi_fake_%s", payments.ErrChargeAlreadyRefunded, order.ID)

	_, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if !errors.Is(err, payments.ErrChargeAlreadyRefunded) {
		t.Fatalf("RefundOrderAs error = %v, want %v", err, payments.ErrChargeAlreadyRefunded)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want paid untouched", got)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "provider_error"); got != 1 {
		t.Errorf("provider_error metric count = %d, want 1", got)
	}
}

func TestRefundOrderAsReplayConvergesWithoutSecondRefund(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)

	first, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if err != nil || first.Status != commerce.OrderStatusRefunded {
		t.Fatalf("first RefundOrderAs = %s %v, want refunded", first.Status, err)
	}
	env.assertStock(t, 5, 4)

	// Replayed POST with the refreshed order: replay branch, no second
	// provider refund, no double stock release.
	replayed, err := env.service.RefundOrderAs(context.Background(), env.mustGetOrder(t, order.ID), commerce.OrderActorAdmin)
	if err != nil || replayed.Status != commerce.OrderStatusRefunded {
		t.Fatalf("replayed RefundOrderAs = %s %v, want refunded no-op", replayed.Status, err)
	}
	if env.provider.createRefundCalls != 1 {
		t.Errorf("CreateRefund calls = %d, want 1", env.provider.createRefundCalls)
	}
	env.assertStock(t, 5, 4)
}

func TestRefundOrderAsAsyncSettlesViaWebhook(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")

	updated, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if err != nil {
		t.Fatalf("RefundOrderAs returned error: %v", err)
	}
	if updated.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("order status = %q, want refund_pending while settling", updated.Status)
	}
	// Unshipped: the reservation is already returned at refund_pending.
	env.assertStock(t, 5, 4)

	event := refundWebhookEvent("evt_refund_settle", "refund.updated", payments.Refund{
		ID:              "re_fake_" + order.ID + "_1",
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusSucceeded,
		AmountCents:     order.TotalCents,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	settled := env.mustGetOrder(t, order.ID)
	if settled.Status != commerce.OrderStatusRefunded || settled.RefundedAt.IsZero() {
		t.Fatalf("order = %s refundedAt %v, want refunded with timestamp", settled.Status, settled.RefundedAt)
	}
	env.assertStock(t, 5, 4)

	// Duplicate delivery no-ops.
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("duplicate ApplyWebhookEvent returned error: %v", err)
	}
	env.assertStock(t, 5, 4)
	if got := env.metrics.count(observability.MetricCheckoutRefund, "settled"); got != 1 {
		t.Errorf("settled metric count = %d, want 1 across the duplicate delivery", got)
	}
}

func TestRefundFailedWebhookThenAdminRetry(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
	if _, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin); err != nil {
		t.Fatalf("RefundOrderAs returned error: %v", err)
	}

	refundID := "re_fake_" + order.ID + "_1"
	event := refundWebhookEvent("evt_refund_fail", "refund.failed", payments.Refund{
		ID:              refundID,
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusFailed,
		FailureReason:   "expired_or_canceled_card",
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	failed := env.mustGetOrder(t, order.ID)
	if failed.Status != commerce.OrderStatusRefundFailed || failed.RefundFailureReason != "expired_or_canceled_card" {
		t.Fatalf("order = %s reason %q, want refund_failed with reason", failed.Status, failed.RefundFailureReason)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "failed"); got != 1 {
		t.Errorf("failed metric count = %d, want 1", got)
	}

	// Sync the fake's view, then the admin retries: attempt 2 mints a fresh
	// refund and the stale failure reason is cleared.
	if err := env.provider.SettleRefund(refundID, payments.RefundStatusFailed, "expired_or_canceled_card"); err != nil {
		t.Fatalf("SettleRefund returned error: %v", err)
	}
	env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
	retried, err := env.service.RefundOrderAs(context.Background(), failed, commerce.OrderActorAdmin)
	if err != nil {
		t.Fatalf("retry RefundOrderAs returned error: %v", err)
	}
	if retried.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("retried order status = %q, want refund_pending", retried.Status)
	}
	stored := env.mustGetOrder(t, order.ID)
	if stored.StripeRefundID != "re_fake_"+order.ID+"_2" || stored.RefundAttempt != 2 {
		t.Errorf("retried refund fields = %q attempt %d, want attempt-2 refund", stored.StripeRefundID, stored.RefundAttempt)
	}
	if stored.RefundFailureReason != "" {
		t.Errorf("RefundFailureReason = %q, want cleared on retry", stored.RefundFailureReason)
	}
}

func TestRefundCreatedWebhookHealsIssueCrashWindow(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)

	// The admin's refund POST crashed after CreateRefund, before the
	// transition: the refund.created webhook heals paid -> refund_pending.
	event := refundWebhookEvent("evt_refund_heal", "refund.created", payments.Refund{
		ID:              "re_fake_" + order.ID + "_1",
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusPending,
		AmountCents:     order.TotalCents,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	healed := env.mustGetOrder(t, order.ID)
	if healed.Status != commerce.OrderStatusRefundPending || healed.StripeRefundID != "re_fake_"+order.ID+"_1" || healed.RefundAttempt != 1 {
		t.Fatalf("healed order = %#v, want adopted refund_pending", healed)
	}
	if last := healed.StatusHistory[len(healed.StatusHistory)-1]; last.Actor != commerce.OrderActorStripe {
		t.Errorf("heal actor = %q, want stripe", last.Actor)
	}
	env.assertStock(t, 5, 4)
	if got := env.metrics.count(observability.MetricCheckoutRefund, "issued"); got != 1 {
		t.Errorf("issued metric count = %d, want 1", got)
	}
}

func TestRefundCreatedWebhookHealOnShippedOrderKeepsStock(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	shippedAt := checkoutTestNow
	if _, err := env.commerceStore.TransitionOrder(context.Background(), order.ID, commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderPatch{Actor: commerce.OrderActorAdmin, ShippedAt: &shippedAt}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}

	event := refundWebhookEvent("evt_refund_heal_shipped", "refund.created", payments.Refund{
		ID:              "re_fake_" + order.ID + "_1",
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusPending,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	healed := env.mustGetOrder(t, order.ID)
	if healed.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("order status = %q, want refund_pending", healed.Status)
	}
	env.assertStock(t, 3, 3)
	if !healed.StockReleasedAt.IsZero() {
		t.Errorf("StockReleasedAt set for a shipped refund heal, want zero")
	}
}

func TestRefundWebhookHealsRetryCrashWindow(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	// Attempt 1 failed.
	env.provider.SetNextRefundOutcome(payments.RefundStatusFailed, "expired_or_canceled_card")
	failed, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if err != nil || failed.Status != commerce.OrderStatusRefundFailed {
		t.Fatalf("RefundOrderAs = %s %v, want refund_failed", failed.Status, err)
	}

	// The admin's retry minted attempt 2 at the provider but crashed before
	// the transition; the newer attempt's webhooks adopt it.
	created := refundWebhookEvent("evt_retry_created", "refund.created", payments.Refund{
		ID:              "re_fake_" + order.ID + "_2",
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         2,
		Status:          payments.RefundStatusPending,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), created); err != nil {
		t.Fatalf("ApplyWebhookEvent(created) returned error: %v", err)
	}
	adopted := env.mustGetOrder(t, order.ID)
	if adopted.Status != commerce.OrderStatusRefundPending || adopted.StripeRefundID != "re_fake_"+order.ID+"_2" || adopted.RefundAttempt != 2 {
		t.Fatalf("adopted order = %#v, want attempt-2 refund_pending", adopted)
	}
	if adopted.RefundFailureReason != "" {
		t.Errorf("RefundFailureReason = %q, want cleared on adoption", adopted.RefundFailureReason)
	}

	updated := refundWebhookEvent("evt_retry_updated", "refund.updated", payments.Refund{
		ID:              "re_fake_" + order.ID + "_2",
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         2,
		Status:          payments.RefundStatusSucceeded,
		AmountCents:     order.TotalCents,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), updated); err != nil {
		t.Fatalf("ApplyWebhookEvent(updated) returned error: %v", err)
	}
	settled := env.mustGetOrder(t, order.ID)
	if settled.Status != commerce.OrderStatusRefunded {
		t.Fatalf("order status = %q, want refunded with no human interaction", settled.Status)
	}
	// Duplicate deliveries no-op.
	if err := env.service.ApplyWebhookEvent(context.Background(), created); err != nil {
		t.Fatalf("duplicate created delivery returned error: %v", err)
	}
	if err := env.service.ApplyWebhookEvent(context.Background(), updated); err != nil {
		t.Fatalf("duplicate updated delivery returned error: %v", err)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusRefunded {
		t.Errorf("order status after duplicates = %q, want refunded", got)
	}
	env.assertStock(t, 5, 4)
}

func TestRefundWebhookDropsStaleAttempts(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	// Attempt 1 failed, attempt 2 pending.
	env.provider.SetNextRefundOutcome(payments.RefundStatusFailed, "expired_or_canceled_card")
	if _, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin); err != nil {
		t.Fatalf("RefundOrderAs returned error: %v", err)
	}
	if err := env.provider.SettleRefund("re_fake_"+order.ID+"_1", payments.RefundStatusFailed, "expired_or_canceled_card"); err != nil {
		t.Fatalf("SettleRefund returned error: %v", err)
	}
	env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
	pending, err := env.service.RefundOrderAs(context.Background(), env.mustGetOrder(t, order.ID), commerce.OrderActorAdmin)
	if err != nil || pending.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("retry RefundOrderAs = %s %v, want refund_pending on attempt 2", pending.Status, err)
	}

	// A late refund.failed for the superseded attempt-1 refund is ignored.
	// (The synchronous attempt-1 failure above already recorded one failed
	// metric; the stale event must not add another.)
	failedBefore := env.metrics.count(observability.MetricCheckoutRefund, "failed")
	stale := refundWebhookEvent("evt_stale_fail", "refund.failed", payments.Refund{
		ID:              "re_fake_" + order.ID + "_1",
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusFailed,
		FailureReason:   "expired_or_canceled_card",
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), stale); err != nil {
		t.Fatalf("ApplyWebhookEvent(stale) returned error: %v", err)
	}
	current := env.mustGetOrder(t, order.ID)
	if current.Status != commerce.OrderStatusRefundPending || current.StripeRefundID != "re_fake_"+order.ID+"_2" {
		t.Fatalf("order = %s %q, want attempt-2 refund_pending unchanged", current.Status, current.StripeRefundID)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "failed"); got != failedBefore {
		t.Errorf("failed metric count = %d, want unchanged %d after the stale event", got, failedBefore)
	}
}

func TestRefundFailedOrderDropsOlderAndUnattributableEvents(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	// Reach refund_failed on attempt 2.
	env.provider.SetNextRefundOutcome(payments.RefundStatusFailed, "expired_or_canceled_card")
	if _, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin); err != nil {
		t.Fatalf("RefundOrderAs returned error: %v", err)
	}
	if err := env.provider.SettleRefund("re_fake_"+order.ID+"_1", payments.RefundStatusFailed, "expired_or_canceled_card"); err != nil {
		t.Fatalf("SettleRefund returned error: %v", err)
	}
	env.provider.SetNextRefundOutcome(payments.RefundStatusFailed, "lost_or_stolen_card")
	failed, err := env.service.RefundOrderAs(context.Background(), env.mustGetOrder(t, order.ID), commerce.OrderActorAdmin)
	if err != nil || failed.Status != commerce.OrderStatusRefundFailed || failed.RefundAttempt != 2 {
		t.Fatalf("retry RefundOrderAs = %#v %v, want refund_failed attempt 2", failed, err)
	}

	for _, event := range []payments.Event{
		refundWebhookEvent("evt_old_attempt", "refund.updated", payments.Refund{
			ID:              "re_fake_" + order.ID + "_1",
			PaymentIntentID: "pi_fake_" + order.ID,
			OrderID:         order.ID,
			Attempt:         1,
			Status:          payments.RefundStatusSucceeded,
		}),
		refundWebhookEvent("evt_foreign_attempt0", "refund.updated", payments.Refund{
			ID:              "re_foreign_dashboard",
			PaymentIntentID: "pi_fake_" + order.ID,
			OrderID:         order.ID,
			Attempt:         0,
			Status:          payments.RefundStatusSucceeded,
		}),
	} {
		if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
			t.Fatalf("ApplyWebhookEvent(%s) returned error: %v", event.ID, err)
		}
	}
	current := env.mustGetOrder(t, order.ID)
	if current.Status != commerce.OrderStatusRefundFailed || current.StripeRefundID != "re_fake_"+order.ID+"_2" {
		t.Fatalf("order = %s %q, want refund_failed attempt 2 unchanged", current.Status, current.StripeRefundID)
	}
}

func TestRefundWebhookPaymentIntentMismatchIsRejected(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	markCallsBefore := env.hooks.markStripeEventCalls

	event := refundWebhookEvent("evt_mismatch", "refund.created", payments.Refund{
		ID:              "re_foreign",
		PaymentIntentID: "pi_someone_else",
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusSucceeded,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error %v, want nil ACK", err)
	}
	current := env.mustGetOrder(t, order.ID)
	if current.Status != commerce.OrderStatusPaid || current.StripeRefundID != "" {
		t.Fatalf("order = %s %q, want paid and untouched", current.Status, current.StripeRefundID)
	}
	env.assertStock(t, 3, 3)
	if got := env.metrics.count(observability.MetricStripeWebhook, "refund_mismatch"); got != 1 {
		t.Errorf("refund_mismatch metric count = %d, want 1", got)
	}
	if env.hooks.markStripeEventCalls != markCallsBefore {
		t.Errorf("event was marked processed, want it left unmarked")
	}
}

func TestRefundWebhookMismatchCheckSkippedForEmptyOrderPI(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	empty := ""
	if _, err := env.commerceStore.PatchOrder(context.Background(), order.ID, commerce.OrderStatusPaid, env.mustGetOrder(t, order.ID).Version, commerce.OrderPatch{StripePaymentIntentID: &empty}); err != nil {
		t.Fatalf("PatchOrder returned error: %v", err)
	}

	event := refundWebhookEvent("evt_skip_check", "refund.created", payments.Refund{
		ID:              "re_fake_" + order.ID + "_1",
		PaymentIntentID: "pi_someone_else",
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusSucceeded,
		AmountCents:     order.TotalCents,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	current := env.mustGetOrder(t, order.ID)
	if current.Status != commerce.OrderStatusRefunded {
		t.Fatalf("order status = %q, want the heal to proceed for the known empty-PI case", current.Status)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "refund_mismatch"); got != 0 {
		t.Errorf("refund_mismatch metric count = %d, want 0", got)
	}
}

func TestRefundAmountMismatchWarnsButSettles(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
	if _, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin); err != nil {
		t.Fatalf("RefundOrderAs returned error: %v", err)
	}

	event := refundWebhookEvent("evt_amount_mismatch", "refund.updated", payments.Refund{
		ID:              "re_fake_" + order.ID + "_1",
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusSucceeded,
		AmountCents:     order.TotalCents - 100,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusRefunded {
		t.Errorf("order status = %q, want refunded despite the amount warning", got)
	}
}

func TestTerminalOrderRefundEventsSettleOrAlarm(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustPlaceOrder(t, checkoutTestLines())
	env.paidSession(t, order.StripeCheckoutSessionID)
	if _, err := env.commerceStore.TransitionOrder(context.Background(), order.ID, commerce.OrderStatusPendingPayment, commerce.OrderStatusCanceled, commerce.OrderPatch{Actor: commerce.OrderActorAdmin}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}
	refundID := "re_fake_" + order.ID + "_1"
	attempt := 1
	canceled := env.mustGetOrder(t, order.ID)
	if _, err := env.commerceStore.PatchOrder(context.Background(), order.ID, commerce.OrderStatusCanceled, canceled.Version, commerce.OrderPatch{StripeRefundID: &refundID, RefundAttempt: &attempt}); err != nil {
		t.Fatalf("PatchOrder returned error: %v", err)
	}

	// Matching-ID success: info ACK, no transition, no failed metric.
	success := refundWebhookEvent("evt_term_ok", "refund.updated", payments.Refund{
		ID:              refundID,
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusSucceeded,
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), success); err != nil {
		t.Fatalf("ApplyWebhookEvent(success) returned error: %v", err)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusCanceled {
		t.Fatalf("order status = %q, want canceled untouched", got)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "failed"); got != 0 {
		t.Errorf("failed metric count = %d, want 0 after the success event", got)
	}

	// Matching-ID failure: alarmed metric + persisted reason, status kept.
	failure := refundWebhookEvent("evt_term_fail", "refund.failed", payments.Refund{
		ID:              refundID,
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Attempt:         1,
		Status:          payments.RefundStatusFailed,
		FailureReason:   "expired_or_canceled_card",
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), failure); err != nil {
		t.Fatalf("ApplyWebhookEvent(failure) returned error: %v", err)
	}
	current := env.mustGetOrder(t, order.ID)
	if current.Status != commerce.OrderStatusCanceled || current.RefundFailureReason != "expired_or_canceled_card" {
		t.Fatalf("order = %s reason %q, want canceled with persisted reason", current.Status, current.RefundFailureReason)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "failed"); got != 1 {
		t.Errorf("failed metric count = %d, want 1", got)
	}

	// Replay converges: reason already set, no second metric or patch.
	if err := env.service.ApplyWebhookEvent(context.Background(), failure); err != nil {
		t.Fatalf("replayed ApplyWebhookEvent(failure) returned error: %v", err)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "failed"); got != 1 {
		t.Errorf("failed metric count after replay = %d, want still 1", got)
	}

	// Non-matching refund ID: warn ACK, no metric, no patch.
	foreign := refundWebhookEvent("evt_term_foreign", "refund.failed", payments.Refund{
		ID:              "re_other",
		PaymentIntentID: "pi_fake_" + order.ID,
		OrderID:         order.ID,
		Status:          payments.RefundStatusFailed,
		FailureReason:   "unknown",
	})
	if err := env.service.ApplyWebhookEvent(context.Background(), foreign); err != nil {
		t.Fatalf("ApplyWebhookEvent(foreign) returned error: %v", err)
	}
	after := env.mustGetOrder(t, order.ID)
	if after.RefundFailureReason != "expired_or_canceled_card" {
		t.Errorf("RefundFailureReason = %q, want unchanged", after.RefundFailureReason)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "failed"); got != 1 {
		t.Errorf("failed metric count after foreign event = %d, want still 1", got)
	}
}

func TestRefundStockReleaseFailureIsRetriedViaReconcile(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	flaky := &flakyStockStore{StockStore: env.catalogStore, failures: 1}
	env.service.Stock = flaky

	updated, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if !errors.Is(err, ErrStockReleaseFailed) {
		t.Fatalf("RefundOrderAs error = %v, want %v", err, ErrStockReleaseFailed)
	}
	if updated.Status != commerce.OrderStatusRefunded {
		t.Fatalf("order status = %q, want refunded (the refund itself committed)", updated.Status)
	}
	stored := env.mustGetOrder(t, order.ID)
	if !stored.StockReleasedAt.IsZero() {
		t.Errorf("StockReleasedAt = %v, want the claim returned after the failed release", stored.StockReleasedAt)
	}
	env.assertStock(t, 3, 3)

	// The admin order-page reconcile re-drives the release exactly once.
	reconciled := env.service.ReconcileRefund(context.Background(), stored)
	if reconciled.Status != commerce.OrderStatusRefunded {
		t.Fatalf("reconciled status = %q, want refunded", reconciled.Status)
	}
	env.assertStock(t, 5, 4)
	if env.mustGetOrder(t, order.ID).StockReleasedAt.IsZero() {
		t.Errorf("StockReleasedAt is zero after the re-driven release")
	}
	// A second reconcile must not release again.
	env.service.ReconcileRefund(context.Background(), env.mustGetOrder(t, order.ID))
	env.assertStock(t, 5, 4)
	if flaky.calls != 2 {
		t.Errorf("AdjustStock calls = %d, want exactly 2 (one failure, one success)", flaky.calls)
	}
}

func TestFinalizePaymentReplayOnRefundFamilyIsNoOp(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	session, err := env.provider.GetSession(context.Background(), order.StripeCheckoutSessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if _, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin); err != nil {
		t.Fatalf("RefundOrderAs returned error: %v", err)
	}

	// A late checkout.session.completed replay must be a no-op success, never
	// ErrOrderNotFinalizable (which would fire a second auto-refund).
	replayed, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment replay error = %v, want nil", err)
	}
	if replayed.Status != commerce.OrderStatusRefunded {
		t.Fatalf("replayed order status = %q, want refunded", replayed.Status)
	}
	event := payments.Event{ID: "evt_late_completed", Type: "checkout.session.completed", SessionID: session.ID, OrderID: order.ID, Session: session}
	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	if got := env.metrics.count(observability.MetricStripeWebhook, "paid_after_terminal"); got != 0 {
		t.Errorf("paid_after_terminal metric count = %d, want 0 for refund-family replays", got)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "issued_auto"); got != 0 {
		t.Errorf("issued_auto metric count = %d, want 0", got)
	}
}

func TestReconcileRefundSettlesPendingRefund(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	env.provider.SetNextRefundOutcome(payments.RefundStatusPending, "")
	pending, err := env.service.RefundOrderAs(context.Background(), order, commerce.OrderActorAdmin)
	if err != nil || pending.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("RefundOrderAs = %s %v, want refund_pending", pending.Status, err)
	}

	// Provider error: degraded to unchanged.
	env.provider.getRefundErr = errors.New("stripe api down")
	unchanged := env.service.ReconcileRefund(context.Background(), env.mustGetOrder(t, order.ID))
	if unchanged.Status != commerce.OrderStatusRefundPending {
		t.Fatalf("reconciled status = %q, want refund_pending on provider error", unchanged.Status)
	}
	env.provider.getRefundErr = nil

	// The refund settles at the provider; reconcile-on-render picks it up.
	if err := env.provider.SettleRefund("re_fake_"+order.ID+"_1", payments.RefundStatusSucceeded, ""); err != nil {
		t.Fatalf("SettleRefund returned error: %v", err)
	}
	settled := env.service.ReconcileRefund(context.Background(), env.mustGetOrder(t, order.ID))
	if settled.Status != commerce.OrderStatusRefunded || settled.RefundedAt.IsZero() {
		t.Fatalf("reconciled order = %s refundedAt %v, want refunded", settled.Status, settled.RefundedAt)
	}
	if got := env.metrics.count(observability.MetricCheckoutRefund, "settled"); got != 1 {
		t.Errorf("settled metric count = %d, want 1", got)
	}
}

func TestReconcileRefundIgnoresNonRefundFamilyOrders(t *testing.T) {
	env := newTestEnv(t)
	order := env.mustPaidOrder(t)
	callsBefore := env.provider.getRefundCalls

	reconciled := env.service.ReconcileRefund(context.Background(), order)
	if reconciled.Status != commerce.OrderStatusPaid {
		t.Fatalf("reconciled status = %q, want paid untouched", reconciled.Status)
	}
	if env.provider.getRefundCalls != callsBefore {
		t.Errorf("GetRefund calls = %d, want %d (no provider lookups)", env.provider.getRefundCalls, callsBefore)
	}
	env.assertStock(t, 3, 3)
}

// --- Guest checkout (CustomerID == "" marker) service tests ---

const guestTestEmail = "guest@example.test"

// guestPlaceOrderInput builds the guest-flow input: zero Customer, contact
// email, GuestAddressID fingerprint surrogate, and a Cart record carrying only
// the cookie-pointer fields (guests have no server CART row).
func (env *testEnv) guestPlaceOrderInput(lines []commerce.OrderLine, email string) PlaceOrderInput {
	return PlaceOrderInput{
		Guest:     true,
		Email:     email,
		AddressID: GuestAddressID(checkoutTestAddress(), email),
		Address:   checkoutTestAddress(),
		Lines:     lines,
	}
}

func (env *testEnv) mustGuestPlaceOrder(t *testing.T, lines []commerce.OrderLine, email string) (string, commerce.Order) {
	t.Helper()
	redirectURL, order, err := env.service.PlaceOrder(context.Background(), env.guestPlaceOrderInput(lines, email))
	if err != nil {
		t.Fatalf("guest PlaceOrder returned error: %v", err)
	}
	return redirectURL, order
}

func TestGuestPlaceOrderHappyPath(t *testing.T) {
	env := newTestEnv(t)
	redirectURL, order := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)

	if order.CustomerID != "" {
		t.Errorf("order customer id = %q, want empty (guest marker)", order.CustomerID)
	}
	if order.Email != guestTestEmail {
		t.Errorf("order email = %q, want %q", order.Email, guestTestEmail)
	}
	if order.CheckoutAttempt != 1 {
		t.Errorf("order checkout attempt = %d, want 1", order.CheckoutAttempt)
	}
	if wantURL := "/checkout/fake-pay?session_id=cs_fake_" + order.ID; redirectURL != wantURL {
		t.Errorf("redirect URL = %q, want %q", redirectURL, wantURL)
	}
	wantFingerprint := env.service.Fingerprint(checkoutTestLines(), GuestAddressID(checkoutTestAddress(), guestTestEmail))
	if order.CartFingerprint != wantFingerprint {
		t.Errorf("order fingerprint = %q, want %q", order.CartFingerprint, wantFingerprint)
	}
	env.assertStock(t, 3, 3)
	if env.provider.ensureCustomerCalls != 0 {
		t.Errorf("EnsureCustomer calls = %d, want 0 (guests get no provider customer)", env.provider.ensureCustomerCalls)
	}
	if env.hooks.getCartCalls != 0 || env.hooks.putCartCalls != 0 {
		t.Errorf("cart store ops = %d gets / %d puts, want 0/0 (guests have no server cart)", env.hooks.getCartCalls, env.hooks.putCartCalls)
	}
}

func TestGuestPlaceOrderValidation(t *testing.T) {
	env := newTestEnv(t)
	withCustomer := env.guestPlaceOrderInput(checkoutTestLines(), guestTestEmail)
	withCustomer.Customer = env.customer
	noEmail := env.guestPlaceOrderInput(checkoutTestLines(), "   ")

	tests := []struct {
		name  string
		input PlaceOrderInput
	}{
		{name: "guest with customer", input: withCustomer},
		{name: "guest without email", input: noEmail},
		{name: "non-guest without customer", input: PlaceOrderInput{Lines: checkoutTestLines()}},
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

func TestGuestPlaceOrderResumesPendingPointer(t *testing.T) {
	env := newTestEnv(t)
	firstURL, first := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)

	resumed := env.guestPlaceOrderInput(checkoutTestLines(), guestTestEmail)
	resumed.Cart = commerce.CartRecord{PendingOrderID: first.ID, PendingFingerprint: first.CartFingerprint}
	secondURL, second, err := env.service.PlaceOrder(context.Background(), resumed)
	if err != nil {
		t.Fatalf("guest re-entry PlaceOrder returned error: %v", err)
	}

	if secondURL != firstURL {
		t.Errorf("re-entry URL = %q, want the original %q", secondURL, firstURL)
	}
	if second.ID != first.ID {
		t.Errorf("re-entry order id = %q, want the original %q", second.ID, first.ID)
	}
	if count := env.orderCount(t); count != 1 {
		t.Errorf("order count = %d, want 1 (no duplicate order)", count)
	}
	env.assertStock(t, 3, 3)
}

func TestGuestPlaceOrderFingerprintMismatchCancelsStale(t *testing.T) {
	changedLines := checkoutTestLines()
	changedLines[0].Quantity = 1
	changedLines[0].LineTotalCents = 1899
	changedAddress := checkoutTestAddress()
	changedAddress.Line1 = "2 Different Street"

	tests := []struct {
		name      string
		lines     []commerce.OrderLine
		address   commerce.OrderAddress
		email     string
		wantStock int
	}{
		{name: "changed lines", lines: changedLines, address: checkoutTestAddress(), email: guestTestEmail, wantStock: 4},
		{name: "changed address", lines: checkoutTestLines(), address: changedAddress, email: guestTestEmail, wantStock: 3},
		{name: "email-only change", lines: checkoutTestLines(), address: checkoutTestAddress(), email: "corrected@example.test", wantStock: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t)
			_, first := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)

			input := PlaceOrderInput{
				Guest:     true,
				Email:     test.email,
				AddressID: GuestAddressID(test.address, test.email),
				Address:   test.address,
				Lines:     test.lines,
				Cart:      commerce.CartRecord{PendingOrderID: first.ID, PendingFingerprint: first.CartFingerprint},
			}
			_, second, err := env.service.PlaceOrder(context.Background(), input)
			if err != nil {
				t.Fatalf("guest PlaceOrder returned error: %v", err)
			}
			if second.ID == first.ID {
				t.Fatalf("expected a fresh order, got the stale one %q", first.ID)
			}
			if second.Email != test.email {
				t.Errorf("fresh order email = %q, want %q stamped", second.Email, test.email)
			}
			stale := env.mustGetOrder(t, first.ID)
			if stale.Status != commerce.OrderStatusCanceled {
				t.Errorf("stale order status = %q, want %q", stale.Status, commerce.OrderStatusCanceled)
			}
			if _, err := env.provider.MarkSessionPaid(first.StripeCheckoutSessionID, false); !errors.Is(err, payments.ErrSessionExpired) {
				t.Errorf("MarkSessionPaid on the stale session error = %v, want %v", err, payments.ErrSessionExpired)
			}
			// Old reservation released, new one held.
			env.assertStock(t, test.wantStock, 3)
		})
	}
}

func TestGuestPointerCannotTouchForeignOrder(t *testing.T) {
	t.Run("guest pointer naming a customer order", func(t *testing.T) {
		env := newTestEnv(t)
		_, foreign := env.mustPlaceOrder(t, checkoutTestLines())

		input := env.guestPlaceOrderInput(checkoutTestLines(), guestTestEmail)
		// Match the foreign order's fingerprint exactly: only the owner check
		// may reject the pointer.
		input.AddressID = "addr00000000000000000000ab"
		input.Cart = commerce.CartRecord{PendingOrderID: foreign.ID, PendingFingerprint: foreign.CartFingerprint}
		_, fresh, err := env.service.PlaceOrder(context.Background(), input)
		if err != nil {
			t.Fatalf("guest PlaceOrder returned error: %v", err)
		}
		if fresh.ID == foreign.ID {
			t.Fatalf("guest pointer resumed a customer order")
		}
		after := env.mustGetOrder(t, foreign.ID)
		if after.Status != foreign.Status || after.Version != foreign.Version || after.StripeCheckoutSessionID != foreign.StripeCheckoutSessionID {
			t.Errorf("foreign order changed: %+v, want untouched %+v", after, foreign)
		}
		if count := env.orderCount(t); count != 2 {
			t.Errorf("order count = %d, want 2", count)
		}
	})

	t.Run("customer pointer naming a guest order", func(t *testing.T) {
		env := newTestEnv(t)
		_, foreign := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)

		input := env.placeOrderInput(t, checkoutTestLines())
		input.AddressID = "addr00000000000000000000ab"
		input.Cart.PendingOrderID = foreign.ID
		input.Cart.PendingFingerprint = foreign.CartFingerprint
		_, fresh, err := env.service.PlaceOrder(context.Background(), input)
		if err != nil {
			t.Fatalf("PlaceOrder returned error: %v", err)
		}
		if fresh.ID == foreign.ID {
			t.Fatalf("customer pointer resumed a guest order")
		}
		after := env.mustGetOrder(t, foreign.ID)
		if after.Status != foreign.Status || after.Version != foreign.Version || after.StripeCheckoutSessionID != foreign.StripeCheckoutSessionID {
			t.Errorf("guest order changed: %+v, want untouched %+v", after, foreign)
		}
	})
}

func TestGuestFinalizePaymentSkipsCartClear(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)
	session := env.paidSession(t, order.StripeCheckoutSessionID)

	finalized, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("FinalizePayment returned error: %v", err)
	}
	if finalized.Status != commerce.OrderStatusPaid {
		t.Errorf("order status = %q, want %q", finalized.Status, commerce.OrderStatusPaid)
	}
	if got := env.metrics.count(observability.MetricCheckoutPayment, "success"); got != 1 {
		t.Errorf("success metric count = %d, want 1", got)
	}
	if env.hooks.getCartCalls != 0 || env.hooks.putCartCalls != 0 {
		t.Errorf("cart store ops after finalize = %d gets / %d puts, want 0/0", env.hooks.getCartCalls, env.hooks.putCartCalls)
	}

	replayed, err := env.service.FinalizePayment(context.Background(), order.ID, session)
	if err != nil {
		t.Fatalf("replayed FinalizePayment returned error: %v", err)
	}
	if replayed.Version != finalized.Version {
		t.Errorf("replay bumped version %d -> %d, want a no-op", finalized.Version, replayed.Version)
	}
	if env.hooks.getCartCalls != 0 || env.hooks.putCartCalls != 0 {
		t.Errorf("cart store ops after replay = %d gets / %d puts, want 0/0", env.hooks.getCartCalls, env.hooks.putCartCalls)
	}
}

func TestGuestWebhookExpiredReleasesStock(t *testing.T) {
	env := newTestEnv(t)
	_, order := env.mustGuestPlaceOrder(t, checkoutTestLines(), guestTestEmail)
	session, err := env.provider.GetSession(context.Background(), order.StripeCheckoutSessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	event := payments.Event{ID: "evt_guest_expired_1", Type: "checkout.session.expired", SessionID: session.ID, OrderID: order.ID, Session: session}

	if err := env.service.ApplyWebhookEvent(context.Background(), event); err != nil {
		t.Fatalf("ApplyWebhookEvent returned error: %v", err)
	}
	if got := env.mustGetOrder(t, order.ID).Status; got != commerce.OrderStatusExpired {
		t.Errorf("order status = %q, want %q", got, commerce.OrderStatusExpired)
	}
	env.assertStock(t, 5, 4)
}

func TestGuestAddressID(t *testing.T) {
	address := checkoutTestAddress()
	base := GuestAddressID(address, guestTestEmail)

	if !strings.HasPrefix(base, "guest-") {
		t.Errorf("GuestAddressID = %q, want the guest- prefix", base)
	}
	if again := GuestAddressID(checkoutTestAddress(), guestTestEmail); again != base {
		t.Errorf("GuestAddressID is not deterministic: %q != %q", again, base)
	}
	if normalized := GuestAddressID(address, "  Guest@Example.TEST "); normalized != base {
		t.Errorf("GuestAddressID is email case/whitespace sensitive: %q != %q", normalized, base)
	}
	if changedEmail := GuestAddressID(address, "other@example.test"); changedEmail == base {
		t.Errorf("GuestAddressID ignored an email change")
	}

	variants := []func(a *commerce.OrderAddress){
		func(a *commerce.OrderAddress) { a.FullName = "Different Name" },
		func(a *commerce.OrderAddress) { a.Line1 = "9 Other Road" },
		func(a *commerce.OrderAddress) { a.Line2 = "Unit 5" },
		func(a *commerce.OrderAddress) { a.City = "Elsewhere" },
		func(a *commerce.OrderAddress) { a.Region = "CA" },
		func(a *commerce.OrderAddress) { a.PostalCode = "90001" },
		func(a *commerce.OrderAddress) { a.Country = "CA" },
		func(a *commerce.OrderAddress) { a.Phone = "+1-555-000-1111" },
	}
	for i, mutate := range variants {
		changed := checkoutTestAddress()
		mutate(&changed)
		if GuestAddressID(changed, guestTestEmail) == base {
			t.Errorf("variant %d: GuestAddressID ignored an address field change", i)
		}
	}
}
