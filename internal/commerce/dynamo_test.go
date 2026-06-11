package commerce

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/appenv"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const testTableName = "commerce-test"

func testDynamoConfig() DynamoConfig {
	return DynamoConfig{
		TableName:               testTableName,
		CustomerOrdersIndexName: DefaultCustomerOrdersIndexName,
		OrdersIndexName:         DefaultOrdersIndexName,
	}
}

func TestNewStoreFromEnvWithRecorderFailsClosedInProduction(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, appenv.EnvironmentProduction)
	t.Setenv(EnvTableName, "")

	store, configured, err := NewStoreFromEnvWithRecorder(context.Background(), nil)
	if !errors.Is(err, ErrCommerceStoreNotConfigured) {
		t.Fatalf("NewStoreFromEnvWithRecorder error = %v, want %v", err, ErrCommerceStoreNotConfigured)
	}
	if store != nil || configured {
		t.Fatalf("store = %#v configured = %v, want nil store and false", store, configured)
	}
}

func TestNewStoreFromEnvWithRecorderFallsBackToMemoryOutsideProduction(t *testing.T) {
	t.Setenv(appenv.EnvAppEnvironment, "development")
	t.Setenv(EnvTableName, "")

	store, configured, err := NewStoreFromEnvWithRecorder(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewStoreFromEnvWithRecorder returned error: %v", err)
	}
	if configured {
		t.Fatalf("configured = true, want false")
	}
	if _, ok := store.(*MemoryStore); !ok {
		t.Fatalf("store = %T, want *MemoryStore", store)
	}
}

func TestDynamoConfigFromEnv(t *testing.T) {
	t.Setenv(EnvTableName, "")
	if _, ok := DynamoConfigFromEnv(); ok {
		t.Fatalf("DynamoConfigFromEnv ok = true with no table name")
	}

	t.Setenv(EnvTableName, "commerce-table")
	t.Setenv(EnvCustomerOrdersIndexName, "")
	t.Setenv(EnvOrdersIndexName, "")
	config, ok := DynamoConfigFromEnv()
	if !ok {
		t.Fatalf("DynamoConfigFromEnv ok = false with table name set")
	}
	if config.TableName != "commerce-table" ||
		config.CustomerOrdersIndexName != DefaultCustomerOrdersIndexName ||
		config.OrdersIndexName != DefaultOrdersIndexName {
		t.Fatalf("config = %#v, want defaults applied", config)
	}

	t.Setenv(EnvCustomerOrdersIndexName, "custom-customer-orders")
	t.Setenv(EnvOrdersIndexName, "custom-orders")
	config, _ = DynamoConfigFromEnv()
	if config.CustomerOrdersIndexName != "custom-customer-orders" || config.OrdersIndexName != "custom-orders" {
		t.Fatalf("config = %#v, want custom index names", config)
	}
}

func TestOnlyOrderItemsCarryGSIAttributes(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	customer := Customer{ID: "cust1", Email: "Shopper@example.test", EmailNormalized: "shopper@example.test", PasswordHash: testPasswordHash, Version: 1, CreatedAt: now, UpdatedAt: now}

	customerAttrs, err := customerItem(customer)
	if err != nil {
		t.Fatalf("customerItem returned error: %v", err)
	}
	lockAttrs, err := emailLockItem(customer)
	if err != nil {
		t.Fatalf("emailLockItem returned error: %v", err)
	}
	sessionAttrs, err := sessionItem(Session{CustomerID: "cust1", TokenHash: "hash", Nonce: "nonce", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("sessionItem returned error: %v", err)
	}
	resetAttrs, err := passwordResetTokenItem(PasswordResetToken{CustomerID: "cust1", TokenHash: "hash", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Version: 1})
	if err != nil {
		t.Fatalf("passwordResetTokenItem returned error: %v", err)
	}
	emailEventAttrs, err := emailEventItem(EmailEvent{Key: "order-placed-v1", Kind: "order_placed", OrderID: "order1", OrderVersion: 1, To: "shopper@example.test", Status: "reserved", Attempts: 1, CreatedAt: now})
	if err != nil {
		t.Fatalf("emailEventItem returned error: %v", err)
	}
	cartAttrs, err := cartItem(CartRecord{CustomerID: "cust1", Version: 1, UpdatedAt: now})
	if err != nil {
		t.Fatalf("cartItem returned error: %v", err)
	}
	addressAttrs, err := addressItem(Address{CustomerID: "cust1", ID: "addr1", FullName: "A Shopper", Line1: "1 Street", City: "Town", Region: "TX", PostalCode: "75001", Country: "US", Version: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("addressItem returned error: %v", err)
	}
	eventAttrs, err := stripeEventItem("evt_1", "checkout.session.completed", "order1", now)
	if err != nil {
		t.Fatalf("stripeEventItem returned error: %v", err)
	}
	orderAttrs, err := orderItem(Order{ID: "order1", CustomerID: "cust1", Email: "shopper@example.test", Status: OrderStatusPendingPayment, Version: 1, TotalCents: 1200, Currency: "usd", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("orderItem returned error: %v", err)
	}

	withoutGSI := map[string]map[string]types.AttributeValue{
		"customer":       customerAttrs,
		"email lock":     lockAttrs,
		"session":        sessionAttrs,
		"password reset": resetAttrs,
		"email event":    emailEventAttrs,
		"cart":           cartAttrs,
		"address":        addressAttrs,
		"stripe event":   eventAttrs,
	}
	for name, attrs := range withoutGSI {
		for _, gsiAttribute := range []string{"gsi1pk", "gsi1sk", "gsi2pk", "gsi2sk"} {
			if _, found := attrs[gsiAttribute]; found {
				t.Fatalf("%s item carries %s; only ORDER rows may have GSI attributes", name, gsiAttribute)
			}
		}
	}

	if got := stringAttribute(orderAttrs, "gsi1pk"); got != "CUSTOMER#cust1" {
		t.Fatalf("order gsi1pk = %q, want CUSTOMER#cust1", got)
	}
	if got := stringAttribute(orderAttrs, "gsi1sk"); got != "ORDER#2026-06-08T12:00:00Z#order1" {
		t.Fatalf("order gsi1sk = %q", got)
	}
	if got := stringAttribute(orderAttrs, "gsi2pk"); got != "ORDERS" {
		t.Fatalf("order gsi2pk = %q, want ORDERS", got)
	}
	if got := stringAttribute(orderAttrs, "gsi2sk"); got != "2026-06-08T12:00:00Z#order1" {
		t.Fatalf("order gsi2sk = %q", got)
	}

	// Guest orders (CustomerID == "") are sparse in gsi1 — DynamoDB rejects
	// empty-string index key values, and a shared "CUSTOMER#" partition would
	// silently pool every guest order — while staying fully present in gsi2
	// for the admin order desk.
	guestAttrs, err := orderItem(Order{ID: "order2", CustomerID: "", Email: "guest@example.test", Status: OrderStatusPendingPayment, Version: 1, TotalCents: 1500, Currency: "usd", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("orderItem (guest) returned error: %v", err)
	}
	for _, gsiAttribute := range []string{"gsi1pk", "gsi1sk"} {
		if _, found := guestAttrs[gsiAttribute]; found {
			t.Fatalf("guest order item carries %s; guest orders must be sparse in gsi1", gsiAttribute)
		}
	}
	if got := stringAttribute(guestAttrs, "gsi2pk"); got != "ORDERS" {
		t.Fatalf("guest order gsi2pk = %q, want ORDERS", got)
	}
	if got := stringAttribute(guestAttrs, "gsi2sk"); got != "2026-06-08T12:00:00Z#order2" {
		t.Fatalf("guest order gsi2sk = %q", got)
	}
}

// TestListOrdersByCustomerPagesAcrossDynamoQueryPages pins queryItems'
// multi-page continuation: with the fake forced to one item per Query call,
// assembling a 2-row page (plus the probe row) requires ExclusiveStartKey
// follow-ups, and the trailing page must follow a LastEvaluatedKey that real
// DynamoDB sets even when the limit cut exactly at the end of the partition.
func TestListOrdersByCustomerPagesAcrossDynamoQueryPages(t *testing.T) {
	ctx := context.Background()
	client := newFakeCommerceClient()
	clock := newTestClock()
	store := NewDynamoStoreWithClock(client, testDynamoConfig(), nil, clock.Now)

	const customerID = "pagingcustomer"
	created := make([]Order, 0, 4)
	for i := 0; i < 4; i++ {
		order, err := store.CreateOrder(ctx, testOrder(customerID, 1000+i))
		if err != nil {
			t.Fatalf("CreateOrder %d returned error: %v", i, err)
		}
		created = append(created, order)
		clock.Advance(time.Hour)
	}

	client.setForcedQueryPageSize(1)

	callsBefore := client.queryCallCount()
	pageOne, err := store.ListOrdersByCustomer(ctx, customerID, 2, OrderCursor{})
	if err != nil {
		t.Fatalf("ListOrdersByCustomer page 1 returned error: %v", err)
	}
	if got := client.queryCallCount() - callsBefore; got < 3 {
		t.Fatalf("page 1 issued %d Query calls, want >= 3 (probe of 3 rows at one item per call)", got)
	}
	if len(pageOne.Orders) != 2 || pageOne.Orders[0].ID != created[3].ID || pageOne.Orders[1].ID != created[2].ID {
		t.Fatalf("page 1 = %v, want the two newest orders %q,%q", pageOne.Orders, created[3].ID, created[2].ID)
	}
	if pageOne.NextCursor.OrderID != created[2].ID || !pageOne.NextCursor.CreatedAt.Equal(created[2].CreatedAt) {
		t.Fatalf("page 1 NextCursor = %#v, want the probe-backed position after %q", pageOne.NextCursor, created[2].ID)
	}

	pageTwo, err := store.ListOrdersByCustomer(ctx, customerID, 2, pageOne.NextCursor)
	if err != nil {
		t.Fatalf("ListOrdersByCustomer page 2 returned error: %v", err)
	}
	if len(pageTwo.Orders) != 2 || pageTwo.Orders[0].ID != created[1].ID || pageTwo.Orders[1].ID != created[0].ID {
		t.Fatalf("page 2 = %v, want the two oldest orders %q,%q", pageTwo.Orders, created[1].ID, created[0].ID)
	}
	if !pageTwo.NextCursor.IsZero() {
		t.Fatalf("page 2 NextCursor = %#v, want zero at the end of the listing", pageTwo.NextCursor)
	}
}

func TestCustomerItemShape(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	attrs, err := customerItem(Customer{ID: "cust1", Email: "Shopper@example.test", EmailNormalized: "shopper@example.test", PasswordHash: testPasswordHash, Version: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("customerItem returned error: %v", err)
	}

	for _, required := range []string{"pk", "sk", "entity_type", "customer_id", "email", "email_normalized", "password_hash", "email_verified", "version", "created_at", "updated_at"} {
		if _, found := attrs[required]; !found {
			t.Fatalf("customer item is missing %q: %#v", required, attrs)
		}
	}
	for _, omitted := range []string{"stripe_customer_id", "default_address_id"} {
		if _, found := attrs[omitted]; found {
			t.Fatalf("customer item includes empty %q", omitted)
		}
	}
	if got := stringAttribute(attrs, "pk"); got != "CUSTOMER#cust1" {
		t.Fatalf("pk = %q", got)
	}
	if got := stringAttribute(attrs, "sk"); got != "CUSTOMER" {
		t.Fatalf("sk = %q", got)
	}
	if got := stringAttribute(attrs, "entity_type"); got != "CUSTOMER" {
		t.Fatalf("entity_type = %q", got)
	}
	verified, ok := attrs["email_verified"].(*types.AttributeValueMemberBOOL)
	if !ok || verified.Value {
		t.Fatalf("email_verified = %#v, want BOOL false persisted", attrs["email_verified"])
	}
}

func TestUnmarshalValidatesEntityType(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	cartAttrs, err := cartItem(CartRecord{CustomerID: "cust1", Version: 1, UpdatedAt: now})
	if err != nil {
		t.Fatalf("cartItem returned error: %v", err)
	}

	if _, err := customerFromItem(cartAttrs); err == nil || !strings.Contains(err.Error(), "not customer") {
		t.Fatalf("customerFromItem error = %v, want entity type rejection", err)
	}
	if _, err := orderFromItem(cartAttrs); err == nil || !strings.Contains(err.Error(), "not order") {
		t.Fatalf("orderFromItem error = %v, want entity type rejection", err)
	}
	if _, err := sessionFromItem(cartAttrs); err == nil || !strings.Contains(err.Error(), "not session") {
		t.Fatalf("sessionFromItem error = %v, want entity type rejection", err)
	}
	if _, err := addressFromItem(cartAttrs); err == nil || !strings.Contains(err.Error(), "not address") {
		t.Fatalf("addressFromItem error = %v, want entity type rejection", err)
	}
	if _, err := emailLockFromItem(cartAttrs); err == nil || !strings.Contains(err.Error(), "not customer email lock") {
		t.Fatalf("emailLockFromItem error = %v, want entity type rejection", err)
	}
	customerAttrs, err := customerItem(Customer{ID: "cust1", Version: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("customerItem returned error: %v", err)
	}
	if _, err := cartFromItem(customerAttrs); err == nil || !strings.Contains(err.Error(), "not cart") {
		t.Fatalf("cartFromItem error = %v, want entity type rejection", err)
	}
}

func TestCreateCustomerClassifiesEmailLockConflictPerItem(t *testing.T) {
	client := newFakeCommerceClient()
	store := NewDynamoStore(client, testDynamoConfig())

	if _, err := store.CreateCustomer(context.Background(), "First@example.test", "first@example.test", testPasswordHash); err != nil {
		t.Fatalf("CreateCustomer returned error: %v", err)
	}

	_, err := store.CreateCustomer(context.Background(), "first@Example.test", "first@example.test", testPasswordHash)
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate email CreateCustomer error = %v, want %v", err, ErrEmailTaken)
	}
}

func TestCreateCustomerClassifiesCustomerRowConflictPerItem(t *testing.T) {
	client := newFakeCommerceClient()
	store := NewDynamoStore(client, testDynamoConfig())
	store.newID = func() string { return "fixedcustomeridfixedcustom" }

	if _, err := store.CreateCustomer(context.Background(), "first@example.test", "first@example.test", testPasswordHash); err != nil {
		t.Fatalf("CreateCustomer returned error: %v", err)
	}

	// Same generated customer id, different email: the customer row condition
	// fails while the email lock condition passes. Per-item classification must
	// NOT surface this as ErrEmailTaken.
	_, err := store.CreateCustomer(context.Background(), "second@example.test", "second@example.test", testPasswordHash)
	if err == nil {
		t.Fatalf("CreateCustomer with colliding id succeeded, want error")
	}
	if errors.Is(err, ErrEmailTaken) {
		t.Fatalf("customer-row conflict misclassified as ErrEmailTaken: %v", err)
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("CreateCustomer error = %v, want customer id conflict", err)
	}
}

func TestOrderItemStockReleasedAtRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	base := Order{ID: "order1", CustomerID: "cust1", Status: OrderStatusCanceled, Version: 2, TotalCents: 1200, Currency: "usd", CreatedAt: now, UpdatedAt: now}

	attrs, err := orderItem(base)
	if err != nil {
		t.Fatalf("orderItem returned error: %v", err)
	}
	if _, found := attrs["stock_released_at"]; found {
		t.Fatalf("zero StockReleasedAt persisted stock_released_at: %#v", attrs["stock_released_at"])
	}

	released := base
	released.StockReleasedAt = now.Add(time.Minute)
	attrs, err = orderItem(released)
	if err != nil {
		t.Fatalf("orderItem returned error: %v", err)
	}
	if got := stringAttribute(attrs, "stock_released_at"); got != "2026-06-08T12:01:00Z" {
		t.Fatalf("stock_released_at = %q, want RFC3339 timestamp", got)
	}
	order, err := orderFromItem(attrs)
	if err != nil || !order.StockReleasedAt.Equal(released.StockReleasedAt) {
		t.Fatalf("orderFromItem StockReleasedAt = %s %v, want %s", order.StockReleasedAt, err, released.StockReleasedAt)
	}
}

func TestClassifyTransactionCancellationRetryableReasons(t *testing.T) {
	canceledWith := func(codes ...string) error {
		reasons := make([]types.CancellationReason, 0, len(codes))
		for _, code := range codes {
			reason := types.CancellationReason{}
			if code != "" {
				reason.Code = aws.String(code)
			}
			reasons = append(reasons, reason)
		}
		return &types.TransactionCanceledException{CancellationReasons: reasons}
	}
	passthrough := errors.New("hard dynamo failure")

	cases := []struct {
		name          string
		err           error
		itemConflicts []error
		wantTransient bool
		want          error
	}{
		{name: "transaction conflict reason", err: canceledWith("TransactionConflict", "None"), wantTransient: true},
		{name: "throttling reason", err: canceledWith("ThrottlingError"), wantTransient: true},
		{name: "provisioned throughput reason", err: canceledWith("None", "ProvisionedThroughputExceeded"), wantTransient: true},
		{name: "missing reason codes alone are not retryable", err: canceledWith("", "")},
		{name: "transaction conflict exception", err: &types.TransactionConflictException{}, wantTransient: true},
		{name: "transaction in progress exception", err: &types.TransactionInProgressException{}, wantTransient: true},
		{name: "conditional check keeps per-item classification", err: canceledWith("None", "ConditionalCheckFailed"), itemConflicts: []error{nil, ErrEmailTaken}, want: ErrEmailTaken},
		{name: "conditional check beats transient reasons", err: canceledWith("TransactionConflict", "ConditionalCheckFailed"), itemConflicts: []error{nil, ErrEmailTaken}, want: ErrEmailTaken},
		{name: "unregistered conditional check is not retryable", err: canceledWith("ConditionalCheckFailed", "TransactionConflict")},
		{name: "unknown reason code is not retryable", err: canceledWith("TransactionConflict", "ValidationError")},
		{name: "unrelated error passes through", err: passthrough},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := classifyTransactionCancellation(testCase.err, testCase.itemConflicts)
			if errors.Is(got, ErrTransientConflict) != testCase.wantTransient {
				t.Fatalf("classifyTransactionCancellation(%v) = %v, want transient=%v", testCase.err, got, testCase.wantTransient)
			}
			if testCase.want != nil && !errors.Is(got, testCase.want) {
				t.Fatalf("classifyTransactionCancellation(%v) = %v, want %v", testCase.err, got, testCase.want)
			}
			if testCase.wantTransient && !errors.Is(got, testCase.err) {
				t.Fatalf("transient classification dropped the underlying error: %v", got)
			}
			if !testCase.wantTransient && testCase.want == nil && !errors.Is(got, testCase.err) {
				t.Fatalf("non-retryable error was rewritten: %v", got)
			}
		})
	}
}

// stubTransactErrorClient injects a TransactWriteItems failure while keeping
// the fake behavior for every other operation.
type stubTransactErrorClient struct {
	*fakeCommerceClient
	err error
}

func (c *stubTransactErrorClient) TransactWriteItems(ctx context.Context, input *dynamodb.TransactWriteItemsInput, options ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	_ = ctx
	_ = input
	_ = options
	return nil, c.err
}

func TestCreateCustomerSurfacesTransientTransactionConflict(t *testing.T) {
	client := &stubTransactErrorClient{
		fakeCommerceClient: newFakeCommerceClient(),
		err: &types.TransactionCanceledException{CancellationReasons: []types.CancellationReason{
			{Code: aws.String("TransactionConflict")},
			{Code: aws.String("None")},
		}},
	}
	store := NewDynamoStore(client, testDynamoConfig())

	_, err := store.CreateCustomer(context.Background(), "shopper@example.test", "shopper@example.test", testPasswordHash)
	if !errors.Is(err, ErrTransientConflict) {
		t.Fatalf("CreateCustomer error = %v, want %v", err, ErrTransientConflict)
	}
	if errors.Is(err, ErrEmailTaken) {
		t.Fatalf("transient conflict misclassified as ErrEmailTaken: %v", err)
	}
}

func TestDynamoStoreRecordsCommerceOperationMetrics(t *testing.T) {
	recorder := &capturingRecorder{}
	client := newFakeCommerceClient()
	store := NewDynamoStoreWithRecorder(client, testDynamoConfig(), recorder)

	if _, _, err := store.GetOrder(context.Background(), "missingorder"); err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}

	names := map[string]bool{}
	for _, metric := range recorder.recorded() {
		names[metric.Name] = true
		if dimensionValue(metric, "Service") != "commerce" {
			t.Fatalf("metric %s Service dimension = %q, want commerce", metric.Name, dimensionValue(metric, "Service"))
		}
		if dimensionValue(metric, "Operation") != "GetItem" || dimensionValue(metric, "Method") != "GetOrder" {
			t.Fatalf("metric dimensions = %#v, want Operation=GetItem Method=GetOrder", metric.Dimensions)
		}
	}
	if !names[observability.MetricCommerceOperation] || !names[observability.MetricCommerceOperationMs] {
		t.Fatalf("recorded metrics = %v, want %s and %s", names, observability.MetricCommerceOperation, observability.MetricCommerceOperationMs)
	}
}

func TestDynamoStoreRecordsOrderTransitionMetric(t *testing.T) {
	recorder := &capturingRecorder{}
	client := newFakeCommerceClient()
	store := NewDynamoStoreWithRecorder(client, testDynamoConfig(), recorder)

	order, err := store.CreateOrder(context.Background(), Order{CustomerID: "cust1", Status: OrderStatusPendingPayment, TotalCents: 100, Currency: "usd"})
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	if _, err := store.TransitionOrder(context.Background(), order.ID, OrderStatusPendingPayment, OrderStatusPaid, OrderPatch{Actor: OrderActorStripe}); err != nil {
		t.Fatalf("TransitionOrder returned error: %v", err)
	}

	for _, metric := range recorder.recorded() {
		if metric.Name != observability.MetricOrderTransition {
			continue
		}
		if dimensionValue(metric, "From") != "pending_payment" || dimensionValue(metric, "To") != "paid" || dimensionValue(metric, "Actor") != "stripe" {
			t.Fatalf("order transition metric dimensions = %#v", metric.Dimensions)
		}
		return
	}
	t.Fatalf("MetricOrderTransition was not recorded")
}

type capturingRecorder struct {
	mu      sync.Mutex
	metrics []observability.Metric
}

func (r *capturingRecorder) Record(metric observability.Metric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics = append(r.metrics, metric)
}

func (r *capturingRecorder) recorded() []observability.Metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observability.Metric(nil), r.metrics...)
}

func dimensionValue(metric observability.Metric, name string) string {
	for _, dimension := range metric.Dimensions {
		if dimension.Name == name {
			return dimension.Value
		}
	}
	return ""
}

// fakeCommerceClient emulates the narrow slice of DynamoDB behavior the
// commerce store exercises: keyed items, the specific condition expressions
// the store issues, SET/REMOVE/ADD/list_append update expressions, table and
// GSI queries, transactional puts with per-item cancellation reasons, and
// batch deletes.
type fakeCommerceClient struct {
	mu                                sync.Mutex
	items                             map[string]map[string]types.AttributeValue
	forcedThrottleConditionalFailures int
	throttleUpdateCalls               int
	deletedKeys                       []string
	capturedExpressions               []string
	// forcedQueryPageSize, when > 0, caps the items each Query call returns
	// regardless of input.Limit, forcing multi-page LastEvaluatedKey
	// continuation exactly like a small real DynamoDB page.
	forcedQueryPageSize int
	queryCalls          int
}

func newFakeCommerceClient() *fakeCommerceClient {
	return &fakeCommerceClient{items: map[string]map[string]types.AttributeValue{}}
}

func fakeItemKey(key map[string]types.AttributeValue) string {
	return stringAttribute(key, "pk") + "\x00" + stringAttribute(key, "sk")
}

// captureExpressions records every condition/update/key-condition expression
// string the store sends so tests can audit them (e.g. for un-aliased
// DynamoDB reserved words). Callers must hold f.mu.
func (f *fakeCommerceClient) captureExpressions(expressions ...*string) {
	for _, expression := range expressions {
		if expression != nil && *expression != "" {
			f.capturedExpressions = append(f.capturedExpressions, *expression)
		}
	}
}

// drainExpressions returns the expressions captured since the previous drain.
func (f *fakeCommerceClient) drainExpressions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	drained := f.capturedExpressions
	f.capturedExpressions = nil
	return drained
}

func (f *fakeCommerceClient) GetItem(ctx context.Context, input *dynamodb.GetItemInput, options ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	_ = ctx
	_ = options
	f.mu.Lock()
	defer f.mu.Unlock()
	return &dynamodb.GetItemOutput{Item: cloneAttributeMap(f.items[fakeItemKey(input.Key)])}, nil
}

func (f *fakeCommerceClient) PutItem(ctx context.Context, input *dynamodb.PutItemInput, options ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	_ = ctx
	_ = options
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captureExpressions(input.ConditionExpression)
	key := fakeItemKey(input.Item)
	if !conditionHolds(aws.ToString(input.ConditionExpression), input.ExpressionAttributeNames, input.ExpressionAttributeValues, f.items[key]) {
		return nil, &types.ConditionalCheckFailedException{}
	}
	f.items[key] = cloneAttributeMap(input.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeCommerceClient) DeleteItem(ctx context.Context, input *dynamodb.DeleteItemInput, options ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	_ = ctx
	_ = options
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captureExpressions(input.ConditionExpression)
	key := fakeItemKey(input.Key)
	f.deletedKeys = append(f.deletedKeys, key)
	delete(f.items, key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeCommerceClient) UpdateItem(ctx context.Context, input *dynamodb.UpdateItemInput, options ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	_ = ctx
	_ = options
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captureExpressions(input.ConditionExpression, input.UpdateExpression)

	if stringAttribute(input.Key, "sk") == throttleSK {
		f.throttleUpdateCalls++
		if f.forcedThrottleConditionalFailures > 0 {
			f.forcedThrottleConditionalFailures--
			return nil, &types.ConditionalCheckFailedException{}
		}
	}

	key := fakeItemKey(input.Key)
	existing := f.items[key]
	if !conditionHolds(aws.ToString(input.ConditionExpression), input.ExpressionAttributeNames, input.ExpressionAttributeValues, existing) {
		return nil, &types.ConditionalCheckFailedException{}
	}

	item := cloneAttributeMap(existing)
	if item == nil {
		item = map[string]types.AttributeValue{}
	}
	applyUpdateExpression(aws.ToString(input.UpdateExpression), input.ExpressionAttributeNames, input.ExpressionAttributeValues, item)
	item["pk"] = input.Key["pk"]
	item["sk"] = input.Key["sk"]
	f.items[key] = cloneAttributeMap(item)
	return &dynamodb.UpdateItemOutput{Attributes: cloneAttributeMap(item)}, nil
}

func (f *fakeCommerceClient) Query(ctx context.Context, input *dynamodb.QueryInput, options ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	_ = ctx
	_ = options
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captureExpressions(input.KeyConditionExpression, input.FilterExpression)

	pkAttribute := "pk"
	skAttribute := "sk"
	if input.IndexName != nil {
		switch {
		case strings.Contains(aws.ToString(input.KeyConditionExpression), "gsi1pk") || input.ExpressionAttributeNames["#gsi1pk"] == "gsi1pk":
			pkAttribute, skAttribute = "gsi1pk", "gsi1sk"
		default:
			pkAttribute, skAttribute = "gsi2pk", "gsi2sk"
		}
	}
	wantPK := stringAttribute(input.ExpressionAttributeValues, ":pk")
	skPrefix := stringAttribute(input.ExpressionAttributeValues, ":sk_prefix")

	type sortableItem struct {
		sortKey string
		item    map[string]types.AttributeValue
	}
	var matches []sortableItem
	for _, item := range f.items {
		if stringAttribute(item, pkAttribute) != wantPK {
			continue
		}
		sortKey := stringAttribute(item, skAttribute)
		if skPrefix != "" && !strings.HasPrefix(sortKey, skPrefix) {
			continue
		}
		matches = append(matches, sortableItem{sortKey: sortKey, item: cloneAttributeMap(item)})
	}
	forward := input.ScanIndexForward == nil || *input.ScanIndexForward
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			less := matches[i].sortKey < matches[j].sortKey
			if less != forward {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	// ExclusiveStartKey is a position, not a row reference: drop everything
	// at or before it in scan order, even when no stored row matches it.
	if input.ExclusiveStartKey != nil {
		startSK := stringAttribute(input.ExclusiveStartKey, skAttribute)
		for len(matches) > 0 {
			if forward && matches[0].sortKey > startSK {
				break
			}
			if !forward && matches[0].sortKey < startSK {
				break
			}
			matches = matches[1:]
		}
	}

	pageCap := 0
	if input.Limit != nil {
		pageCap = int(*input.Limit)
	}
	if f.forcedQueryPageSize > 0 && (pageCap == 0 || f.forcedQueryPageSize < pageCap) {
		pageCap = f.forcedQueryPageSize
	}
	f.queryCalls++

	output := &dynamodb.QueryOutput{}
	if pageCap > 0 && len(matches) >= pageCap {
		// The page was cut at the cap. Real DynamoDB sets LastEvaluatedKey in
		// this case even when nothing happens to remain, so the caller must
		// issue a follow-up query to learn the partition is exhausted.
		matches = matches[:pageCap]
		last := matches[len(matches)-1].item
		output.LastEvaluatedKey = map[string]types.AttributeValue{
			"pk": last["pk"],
			"sk": last["sk"],
		}
		if input.IndexName != nil {
			output.LastEvaluatedKey[pkAttribute] = last[pkAttribute]
			output.LastEvaluatedKey[skAttribute] = last[skAttribute]
		}
	}
	for _, match := range matches {
		output.Items = append(output.Items, match.item)
	}
	return output, nil
}

func (f *fakeCommerceClient) TransactWriteItems(ctx context.Context, input *dynamodb.TransactWriteItemsInput, options ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	_ = ctx
	_ = options
	f.mu.Lock()
	defer f.mu.Unlock()

	reasons := make([]types.CancellationReason, len(input.TransactItems))
	failed := false
	for index, transactItem := range input.TransactItems {
		put := transactItem.Put
		if put == nil {
			panic("fakeCommerceClient only supports Put transact items")
		}
		f.captureExpressions(put.ConditionExpression)
		key := fakeItemKey(put.Item)
		if !conditionHolds(aws.ToString(put.ConditionExpression), put.ExpressionAttributeNames, put.ExpressionAttributeValues, f.items[key]) {
			reasons[index] = types.CancellationReason{Code: aws.String("ConditionalCheckFailed")}
			failed = true
			continue
		}
		reasons[index] = types.CancellationReason{Code: aws.String("None")}
	}
	if failed {
		return nil, &types.TransactionCanceledException{CancellationReasons: reasons}
	}
	for _, transactItem := range input.TransactItems {
		f.items[fakeItemKey(transactItem.Put.Item)] = cloneAttributeMap(transactItem.Put.Item)
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func (f *fakeCommerceClient) BatchWriteItem(ctx context.Context, input *dynamodb.BatchWriteItemInput, options ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	_ = ctx
	_ = options
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, requests := range input.RequestItems {
		for _, request := range requests {
			if request.DeleteRequest == nil {
				panic("fakeCommerceClient only supports delete batch requests")
			}
			delete(f.items, fakeItemKey(request.DeleteRequest.Key))
		}
	}
	return &dynamodb.BatchWriteItemOutput{}, nil
}

func (f *fakeCommerceClient) setForcedQueryPageSize(size int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forcedQueryPageSize = size
}

func (f *fakeCommerceClient) queryCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queryCalls
}

func (f *fakeCommerceClient) itemForKey(t *testing.T, pk string, sk string) map[string]types.AttributeValue {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	item := cloneAttributeMap(f.items[pk+"\x00"+sk])
	if item == nil {
		t.Fatalf("missing item for pk %q sk %q", pk, sk)
	}
	return item
}

func (f *fakeCommerceClient) hasItem(pk string, sk string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, found := f.items[pk+"\x00"+sk]
	return found
}

func resolveAttributeName(token string, names map[string]string) string {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, "#") {
		resolved, found := names[token]
		if !found {
			panic("unresolved expression attribute name: " + token)
		}
		return resolved
	}
	return token
}

func conditionHolds(condition string, names map[string]string, values map[string]types.AttributeValue, item map[string]types.AttributeValue) bool {
	condition = strings.TrimSpace(condition)
	if condition == "" {
		return true
	}
	if strings.Contains(condition, "first_failed_at") {
		return throttleConditionHolds(condition, values, item)
	}
	for _, orPart := range strings.Split(condition, " OR ") {
		holds := true
		for _, clause := range strings.Split(orPart, " AND ") {
			if !conditionAtomHolds(strings.TrimSpace(clause), names, values, item) {
				holds = false
				break
			}
		}
		if holds {
			return true
		}
	}
	return false
}

func conditionAtomHolds(clause string, names map[string]string, values map[string]types.AttributeValue, item map[string]types.AttributeValue) bool {
	switch {
	case strings.HasPrefix(clause, "attribute_not_exists(") && strings.HasSuffix(clause, ")"):
		attribute := resolveAttributeName(clause[len("attribute_not_exists("):len(clause)-1], names)
		_, found := item[attribute]
		return !found
	case strings.HasPrefix(clause, "attribute_exists(") && strings.HasSuffix(clause, ")"):
		attribute := resolveAttributeName(clause[len("attribute_exists("):len(clause)-1], names)
		_, found := item[attribute]
		return found
	default:
		if left, right, found := strings.Cut(clause, " > "); found {
			attribute := resolveAttributeName(left, names)
			return numberAttribute(item, attribute) > numberAttribute(values, strings.TrimSpace(right))
		}
		left, right, found := strings.Cut(clause, " = ")
		if !found {
			panic("unhandled condition clause: " + clause)
		}
		attribute := resolveAttributeName(left, names)
		return attributeValuesEqual(item[attribute], values[strings.TrimSpace(right)])
	}
}

func throttleConditionHolds(condition string, values map[string]types.AttributeValue, item map[string]types.AttributeValue) bool {
	_, exists := item["pk"]
	firstFailedAt := numberAttribute(item, "first_failed_at")
	failureCount := numberAttribute(item, "failure_count")
	lockedUntil := numberAttribute(item, "locked_until")
	now := numberAttribute(values, ":now")
	windowStart := numberAttribute(values, ":window_start")
	zero := numberAttribute(values, ":zero")
	switch {
	case strings.Contains(condition, "attribute_not_exists(pk)"):
		return !exists || (firstFailedAt <= windowStart && (lockedUntil <= zero || lockedUntil <= now)) || (lockedUntil <= now && lockedUntil > zero)
	case strings.Contains(condition, "failure_count < :lock_threshold"):
		return exists && firstFailedAt > windowStart && failureCount < numberAttribute(values, ":lock_threshold") && (lockedUntil <= zero || lockedUntil <= now)
	case strings.Contains(condition, "failure_count = :lock_threshold"):
		return exists && firstFailedAt > windowStart && failureCount == numberAttribute(values, ":lock_threshold") && (lockedUntil <= zero || lockedUntil <= now)
	default:
		panic("unhandled throttle condition expression: " + condition)
	}
}

func attributeValuesEqual(left types.AttributeValue, right types.AttributeValue) bool {
	switch leftValue := left.(type) {
	case *types.AttributeValueMemberS:
		rightValue, ok := right.(*types.AttributeValueMemberS)
		return ok && leftValue.Value == rightValue.Value
	case *types.AttributeValueMemberN:
		rightValue, ok := right.(*types.AttributeValueMemberN)
		if !ok {
			return false
		}
		leftNumber, leftErr := strconv.ParseInt(leftValue.Value, 10, 64)
		rightNumber, rightErr := strconv.ParseInt(rightValue.Value, 10, 64)
		return leftErr == nil && rightErr == nil && leftNumber == rightNumber
	case nil:
		return false
	default:
		panic("unhandled attribute value comparison")
	}
}

func applyUpdateExpression(expression string, names map[string]string, values map[string]types.AttributeValue, item map[string]types.AttributeValue) {
	rest := strings.TrimPrefix(expression, "SET ")
	setPart := rest
	removePart := ""
	addPart := ""
	if index := strings.Index(rest, " REMOVE "); index >= 0 {
		setPart = rest[:index]
		removePart = rest[index+len(" REMOVE "):]
	} else if index := strings.Index(rest, " ADD "); index >= 0 {
		setPart = rest[:index]
		addPart = rest[index+len(" ADD "):]
	}

	for _, assignment := range splitTopLevel(setPart, ',') {
		path, valueExpression, found := strings.Cut(assignment, " = ")
		if !found {
			panic("unhandled SET assignment: " + assignment)
		}
		attribute := resolveAttributeName(path, names)
		valueExpression = strings.TrimSpace(valueExpression)
		if strings.HasPrefix(valueExpression, "list_append(") {
			inner := valueExpression[len("list_append(") : len(valueExpression)-1]
			arguments := splitTopLevel(inner, ',')
			if len(arguments) != 2 {
				panic("unhandled list_append: " + valueExpression)
			}
			existing := []types.AttributeValue{}
			if current, ok := item[attribute].(*types.AttributeValueMemberL); ok {
				existing = append(existing, current.Value...)
			}
			appended, ok := values[strings.TrimSpace(arguments[1])].(*types.AttributeValueMemberL)
			if !ok {
				panic("list_append value is not a list: " + arguments[1])
			}
			item[attribute] = &types.AttributeValueMemberL{Value: append(existing, appended.Value...)}
			continue
		}
		value, found := values[valueExpression]
		if !found {
			panic("unresolved expression attribute value: " + valueExpression)
		}
		item[attribute] = value
	}
	if removePart != "" {
		for _, attributeToken := range splitTopLevel(removePart, ',') {
			delete(item, resolveAttributeName(attributeToken, names))
		}
	}
	if addPart != "" {
		fields := strings.Fields(addPart)
		if len(fields) != 2 {
			panic("unhandled ADD clause: " + addPart)
		}
		attribute := resolveAttributeName(fields[0], names)
		item[attribute] = numberValue(numberAttribute(item, attribute) + numberAttribute(values, fields[1]))
	}
}

func splitTopLevel(value string, separator rune) []string {
	var parts []string
	depth := 0
	current := strings.Builder{}
	for _, character := range value {
		switch character {
		case '(':
			depth++
		case ')':
			depth--
		}
		if character == separator && depth == 0 {
			parts = append(parts, strings.TrimSpace(current.String()))
			current.Reset()
			continue
		}
		current.WriteRune(character)
	}
	if trimmed := strings.TrimSpace(current.String()); trimmed != "" {
		parts = append(parts, trimmed)
	}
	return parts
}

func stringAttribute(item map[string]types.AttributeValue, name string) string {
	value, ok := item[name].(*types.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func cloneAttributeMap(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	if item == nil {
		return nil
	}
	clone := make(map[string]types.AttributeValue, len(item))
	for key, value := range item {
		clone[key] = value
	}
	return clone
}
