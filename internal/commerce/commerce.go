// Package commerce holds the customer/session/cart/address/order domain and
// the DynamoDB-backed store for the thailandgiftshop-commerce table.
package commerce

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
)

const (
	EnvTableName               = "COMMERCE_TABLE_NAME"
	EnvCustomerOrdersIndexName = "COMMERCE_CUSTOMER_ORDERS_INDEX_NAME"
	EnvOrdersIndexName         = "COMMERCE_ORDERS_INDEX_NAME"
	EnvSessionSecret           = "CUSTOMER_SESSION_SECRET"

	SessionCookieName    = "__Host-tgs_customer"
	CSRFCookieName       = "__Host-tgs_customer_csrf"
	GuestCSRFCookieName  = "__Host-tgs_guest_csrf"
	GuestOrderCookieName = "__Host-tgs_guest_order"

	DefaultCustomerOrdersIndexName = "customer-orders-index"
	DefaultOrdersIndexName         = "orders-index"
)

// MaxAddressesPerCustomer caps how many saved addresses one customer may keep.
const MaxAddressesPerCustomer = 10

var (
	ErrEmailTaken                 = errors.New("commerce email already registered")
	ErrVersionConflict            = errors.New("commerce version conflict")
	ErrOrderTransitionConflict    = errors.New("commerce order transition conflict")
	ErrAddressLimit               = errors.New("commerce address limit reached")
	ErrCommerceStoreNotConfigured = errors.New("commerce store not configured")
	// ErrTransientConflict marks retryable transaction contention — a
	// transaction-vs-transaction conflict on overlapping items, throttling, or
	// an already-in-progress transaction — that a bounded retry can resolve.
	// Condition failures keep their exact per-item classification (for example
	// ErrEmailTaken) and are never wrapped in ErrTransientConflict.
	ErrTransientConflict = errors.New("commerce transient transaction conflict")
)

// NormalizeEmail lowercases and trims an email address for uniqueness checks
// and lookups. Display casing is preserved separately on Customer.Email.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

type Customer struct {
	ID               string
	Email            string
	EmailNormalized  string
	PasswordHash     string
	EmailVerified    bool
	StripeCustomerID string
	DefaultAddressID string
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Session is a server-side customer session row. TokenHash is the lowercase
// hex SHA-256 of the raw cookie token; the raw token is never stored. DynamoDB
// TTL expiry is lazy, so readers must re-check ExpiresAt against the clock.
type Session struct {
	CustomerID string
	TokenHash  string
	Nonce      string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// CartRecord is the authoritative signed-in cart. Lines reuse the exact
// cart.Line shape so the tgs_cart cookie mirror round-trips losslessly.
type CartRecord struct {
	CustomerID         string
	Lines              []cart.Line
	PendingOrderID     string
	PendingFingerprint string
	Version            int
	UpdatedAt          time.Time
}

type Address struct {
	CustomerID string
	ID         string
	FullName   string
	Line1      string
	Line2      string
	City       string
	Region     string
	PostalCode string
	Country    string
	Phone      string
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type OrderStatus string

const (
	OrderStatusPendingPayment OrderStatus = "pending_payment"
	OrderStatusPaid           OrderStatus = "paid"
	OrderStatusShipped        OrderStatus = "shipped"
	OrderStatusDelivered      OrderStatus = "delivered"
	OrderStatusPaymentFailed  OrderStatus = "payment_failed"
	OrderStatusExpired        OrderStatus = "expired"
	OrderStatusCanceled       OrderStatus = "canceled"
	OrderStatusRefundPending  OrderStatus = "refund_pending"
	OrderStatusRefunded       OrderStatus = "refunded"
	OrderStatusRefundFailed   OrderStatus = "refund_failed"
)

// OrderTransitions is the single source of truth for the order state machine:
//
//	pending_payment -> paid | payment_failed | expired | canceled
//	paid            -> shipped | refund_pending
//	shipped         -> delivered | refund_pending
//	delivered       -> refund_pending
//	refund_pending  -> refunded | refund_failed
//	refund_failed   -> refund_pending            (admin retry)
//	refunded, payment_failed, expired, canceled are terminal.
var OrderTransitions = map[OrderStatus][]OrderStatus{
	OrderStatusPendingPayment: {OrderStatusPaid, OrderStatusPaymentFailed, OrderStatusExpired, OrderStatusCanceled},
	OrderStatusPaid:           {OrderStatusShipped, OrderStatusRefundPending},
	OrderStatusShipped:        {OrderStatusDelivered, OrderStatusRefundPending},
	OrderStatusDelivered:      {OrderStatusRefundPending},
	OrderStatusRefundPending:  {OrderStatusRefunded, OrderStatusRefundFailed},
	OrderStatusRefundFailed:   {OrderStatusRefundPending},
	OrderStatusRefunded:       {},
	OrderStatusPaymentFailed:  {},
	OrderStatusExpired:        {},
	OrderStatusCanceled:       {},
}

func AllowedOrderTransition(from OrderStatus, to OrderStatus) bool {
	for _, allowed := range OrderTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

func ValidOrderStatus(status OrderStatus) bool {
	_, known := OrderTransitions[status]
	return known
}

// Actors recorded in an order's status history.
const (
	OrderActorCustomer = "customer"
	OrderActorStripe   = "stripe"
	OrderActorAdmin    = "admin"
	OrderActorSystem   = "system"
)

// OrderLine is a frozen purchase-time snapshot; prices and names never track
// later catalog edits.
type OrderLine struct {
	Slug           string
	ProductID      string
	Name           string
	VariantID      string
	VariantLabel   string
	UnitPriceCents int
	Quantity       int
	LineTotalCents int
	ImageURL       string
}

// OrderAddress is the shipping address snapshot embedded on the order.
type OrderAddress struct {
	FullName   string
	Line1      string
	Line2      string
	City       string
	Region     string
	PostalCode string
	Country    string
	Phone      string
}

type StatusEvent struct {
	Status OrderStatus
	At     time.Time
	Actor  string
}

type Order struct {
	ID                      string
	CustomerID              string
	Email                   string
	Status                  OrderStatus
	Version                 int
	Lines                   []OrderLine
	SubtotalCents           int
	ShippingCents           int
	TaxCents                int
	TotalCents              int
	Currency                string
	ShippingAddress         OrderAddress
	CartFingerprint         string
	StripeCheckoutSessionID string
	StripePaymentIntentID   string
	PaymentCardBrand        string
	PaymentCardLast4        string
	TrackingCarrier         string
	TrackingNumber          string
	// StripeRefundID is the latest provider refund issued for this order.
	StripeRefundID string
	// RefundAttempt scopes the provider refund idempotency key (the
	// CheckoutAttempt precedent): a retry after a failed refund mints a fresh
	// key by incrementing it.
	RefundAttempt int
	// RefundFailureReason carries the provider failure_reason of the latest
	// failed refund; cleared on every transition back to refund_pending.
	RefundFailureReason string
	CheckoutAttempt     int
	StatusHistory       []StatusEvent
	CreatedAt           time.Time
	UpdatedAt           time.Time
	PaidAt              time.Time
	ShippedAt           time.Time
	DeliveredAt         time.Time
	// RefundedAt is zero until the refund settles at the provider.
	RefundedAt time.Time
	// StockReleasedAt marks that the reserved stock for this order has been
	// (or is being) released back to the catalog; the zero value means no
	// release has been confirmed. The checkout release-claim protocol SETs it
	// when claiming the release and CLEARs it again if the release fails.
	StockReleasedAt time.Time
}

// OrderPatch carries the optional field updates applied alongside
// TransitionOrder and PatchOrder. Nil pointers leave fields untouched; a
// pointer to the zero value clears the field. Actor labels the status-history
// entry appended by TransitionOrder and defaults to "system".
type OrderPatch struct {
	Actor                   string
	StripeCheckoutSessionID *string
	StripePaymentIntentID   *string
	PaymentCardBrand        *string
	PaymentCardLast4        *string
	TrackingCarrier         *string
	TrackingNumber          *string
	StripeRefundID          *string
	RefundAttempt           *int
	RefundFailureReason     *string
	CheckoutAttempt         *int
	PaidAt                  *time.Time
	ShippedAt               *time.Time
	DeliveredAt             *time.Time
	RefundedAt              *time.Time
	// StockReleasedAt follows the shared pointer convention: nil leaves the
	// stored marker untouched, a pointer to the zero time clears (REMOVEs) it,
	// and a pointer to any other time sets it.
	StockReleasedAt *time.Time
}

// OrderCursor resumes a newest-first order listing immediately after the
// order identified by OrderID and CreatedAt — the two components of the
// order GSI sort keys ("ORDER#<RFC3339 created>#<id>" on gsi1,
// "<RFC3339 created>#<id>" on gsi2). The zero value means "start at the
// newest order". Cursor times are UTC at RFC3339 second precision, matching
// formatCommerceTime; CreatedAt is immutable, so cursors are stable forever.
type OrderCursor struct {
	OrderID   string
	CreatedAt time.Time
}

// IsZero reports whether the cursor is the start-of-listing marker. A cursor
// missing either component is treated as the start so a half-built value can
// never address an arbitrary position.
func (c OrderCursor) IsZero() bool {
	return c.OrderID == "" || c.CreatedAt.IsZero()
}

// OrderPage is one newest-first page of orders. NextCursor resumes the
// listing after the last order in Orders; it is the zero cursor when no
// further order existed at query time, so a non-zero NextCursor always leads
// to a non-empty page (orders are never deleted).
type OrderPage struct {
	Orders     []Order
	NextCursor OrderCursor
}

// probeLimit fetches one row beyond the requested page so NextCursor is only
// emitted when a further row provably exists (never a dangling "older" link).
// limit <= 0 keeps the existing fetch-everything contract.
func probeLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	return limit + 1
}

// orderPageFromProbe converts a probe-sized listing into a page: when the
// probe row is present, the page is trimmed back to limit and NextCursor
// points after its last visible order.
func orderPageFromProbe(orders []Order, limit int) OrderPage {
	if limit <= 0 || len(orders) <= limit {
		return OrderPage{Orders: orders}
	}
	page := orders[:limit:limit]
	last := page[limit-1]
	return OrderPage{Orders: page, NextCursor: OrderCursor{OrderID: last.ID, CreatedAt: last.CreatedAt}}
}

func orderActorOrDefault(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return OrderActorSystem
	}
	return actor
}

func applyOrderPatchFields(order *Order, patch OrderPatch) {
	if patch.StripeCheckoutSessionID != nil {
		order.StripeCheckoutSessionID = *patch.StripeCheckoutSessionID
	}
	if patch.StripePaymentIntentID != nil {
		order.StripePaymentIntentID = *patch.StripePaymentIntentID
	}
	if patch.PaymentCardBrand != nil {
		order.PaymentCardBrand = *patch.PaymentCardBrand
	}
	if patch.PaymentCardLast4 != nil {
		order.PaymentCardLast4 = *patch.PaymentCardLast4
	}
	if patch.TrackingCarrier != nil {
		order.TrackingCarrier = *patch.TrackingCarrier
	}
	if patch.TrackingNumber != nil {
		order.TrackingNumber = *patch.TrackingNumber
	}
	if patch.StripeRefundID != nil {
		order.StripeRefundID = *patch.StripeRefundID
	}
	if patch.RefundAttempt != nil {
		order.RefundAttempt = *patch.RefundAttempt
	}
	if patch.RefundFailureReason != nil {
		order.RefundFailureReason = *patch.RefundFailureReason
	}
	if patch.CheckoutAttempt != nil {
		order.CheckoutAttempt = *patch.CheckoutAttempt
	}
	if patch.PaidAt != nil {
		order.PaidAt = *patch.PaidAt
	}
	if patch.ShippedAt != nil {
		order.ShippedAt = *patch.ShippedAt
	}
	if patch.DeliveredAt != nil {
		order.DeliveredAt = *patch.DeliveredAt
	}
	if patch.RefundedAt != nil {
		order.RefundedAt = *patch.RefundedAt
	}
	if patch.StockReleasedAt != nil {
		order.StockReleasedAt = *patch.StockReleasedAt
	}
}

type Store interface {
	CreateCustomer(ctx context.Context, email string, emailNormalized string, passwordHash string) (Customer, error)
	GetCustomerByEmail(ctx context.Context, emailNormalized string) (Customer, bool, error)
	GetCustomerByID(ctx context.Context, id string) (Customer, bool, error)
	SetStripeCustomerID(ctx context.Context, customerID string, stripeID string) (Customer, error)
	SetDefaultAddress(ctx context.Context, customerID string, addressID string, expectedVersion int) error
	UpdatePassword(ctx context.Context, customerID string, newHash string, expectedVersion int) error

	PutSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, customerID string, tokenHash string) (Session, bool, error)
	DeleteSession(ctx context.Context, customerID string, tokenHash string) error
	DeleteAllSessions(ctx context.Context, customerID string) error

	GetCart(ctx context.Context, customerID string) (CartRecord, bool, error)
	PutCart(ctx context.Context, c CartRecord) (CartRecord, error)

	CreateAddress(ctx context.Context, a Address) (Address, error)
	UpdateAddress(ctx context.Context, a Address, expectedVersion int) (Address, error)
	DeleteAddress(ctx context.Context, customerID string, addressID string) error
	GetAddress(ctx context.Context, customerID string, addressID string) (Address, bool, error)
	ListAddresses(ctx context.Context, customerID string) ([]Address, error)

	CreateOrder(ctx context.Context, o Order) (Order, error)
	GetOrder(ctx context.Context, orderID string) (Order, bool, error)
	// ListOrdersByCustomer returns one newest-first page of the customer's
	// orders, resuming after cursor. limit <= 0 returns the full listing.
	// customerID == "" (guest marker) always returns an empty page: guest
	// orders are sparse in gsi1 and must never be listable as a
	// pseudo-customer.
	ListOrdersByCustomer(ctx context.Context, customerID string, limit int, cursor OrderCursor) (OrderPage, error)
	// ListOrders returns one newest-first page of all orders (admin order
	// desk, gsi2 — includes guest orders), resuming after cursor. limit <= 0
	// returns the full listing.
	ListOrders(ctx context.Context, limit int, cursor OrderCursor) (OrderPage, error)
	TransitionOrder(ctx context.Context, orderID string, from OrderStatus, to OrderStatus, patch OrderPatch) (Order, error)
	PatchOrder(ctx context.Context, orderID string, expectedStatus OrderStatus, expectedVersion int, patch OrderPatch) (Order, error)

	MarkStripeEventProcessed(ctx context.Context, eventID string, eventType string, orderID string) (alreadySeen bool, err error)

	ReserveLoginAttempt(ctx context.Context, key string, now time.Time) (ThrottleDecision, error)
	ClearLoginAttempts(ctx context.Context, keys []string) error
}
