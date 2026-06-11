package commerce

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/signedtoken"
)

var _ Store = (*MemoryStore)(nil)

// MemoryStore is the behavior-identical in-memory Store used by local dev,
// demo mode, and tests.
type MemoryStore struct {
	mu           sync.Mutex
	now          func() time.Time
	newID        func() string
	customers    map[string]Customer
	emailLocks   map[string]string
	sessions     map[string]map[string]Session
	carts        map[string]CartRecord
	addresses    map[string]map[string]Address
	orders       map[string]Order
	stripeEvents map[string]bool
	throttle     map[string]memoryThrottleRecord
}

func NewMemoryStore() *MemoryStore {
	return NewMemoryStoreWithClock(time.Now)
}

func NewMemoryStoreWithClock(now func() time.Time) *MemoryStore {
	return &MemoryStore{
		now:          now,
		newID:        signedtoken.NewID,
		customers:    map[string]Customer{},
		emailLocks:   map[string]string{},
		sessions:     map[string]map[string]Session{},
		carts:        map[string]CartRecord{},
		addresses:    map[string]map[string]Address{},
		orders:       map[string]Order{},
		stripeEvents: map[string]bool{},
		throttle:     map[string]memoryThrottleRecord{},
	}
}

func (s *MemoryStore) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s *MemoryStore) generateID() string {
	if s.newID == nil {
		return signedtoken.NewID()
	}
	return s.newID()
}

func (s *MemoryStore) CreateCustomer(ctx context.Context, email string, emailNormalized string, passwordHash string) (Customer, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, taken := s.emailLocks[emailNormalized]; taken {
		return Customer{}, fmt.Errorf("%w", ErrEmailTaken)
	}

	now := s.clock()
	customer := Customer{
		ID:              s.generateID(),
		Email:           email,
		EmailNormalized: emailNormalized,
		PasswordHash:    passwordHash,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if _, exists := s.customers[customer.ID]; exists {
		return Customer{}, fmt.Errorf("commerce customer id %q already exists", customer.ID)
	}
	s.customers[customer.ID] = customer
	s.emailLocks[emailNormalized] = customer.ID

	return customer, nil
}

func (s *MemoryStore) GetCustomerByEmail(ctx context.Context, emailNormalized string) (Customer, bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	customerID, found := s.emailLocks[emailNormalized]
	if !found {
		return Customer{}, false, nil
	}
	customer, found := s.customers[customerID]
	return customer, found, nil
}

func (s *MemoryStore) GetCustomerByID(ctx context.Context, id string) (Customer, bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	customer, found := s.customers[id]
	return customer, found, nil
}

func (s *MemoryStore) SetStripeCustomerID(ctx context.Context, customerID string, stripeID string) (Customer, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	customer, found := s.customers[customerID]
	if !found {
		return Customer{}, fmt.Errorf("commerce customer %q not found", customerID)
	}
	if customer.StripeCustomerID != "" {
		// Already set (lost race): return the winner.
		return customer, nil
	}
	customer.StripeCustomerID = stripeID
	customer.UpdatedAt = s.clock()
	s.customers[customerID] = customer
	return customer, nil
}

func (s *MemoryStore) SetDefaultAddress(ctx context.Context, customerID string, addressID string, expectedVersion int) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	customer, found := s.customers[customerID]
	if !found || customer.Version != expectedVersion {
		return fmt.Errorf("%w", ErrVersionConflict)
	}
	customer.DefaultAddressID = addressID
	customer.Version = expectedVersion + 1
	customer.UpdatedAt = s.clock()
	s.customers[customerID] = customer
	return nil
}

func (s *MemoryStore) UpdatePassword(ctx context.Context, customerID string, newHash string, expectedVersion int) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	customer, found := s.customers[customerID]
	if !found || customer.Version != expectedVersion {
		return fmt.Errorf("%w", ErrVersionConflict)
	}
	customer.PasswordHash = newHash
	customer.Version = expectedVersion + 1
	customer.UpdatedAt = s.clock()
	s.customers[customerID] = customer
	return nil
}

func (s *MemoryStore) PutSession(ctx context.Context, session Session) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessions[session.CustomerID] == nil {
		s.sessions[session.CustomerID] = map[string]Session{}
	}
	s.sessions[session.CustomerID][session.TokenHash] = session
	return nil
}

func (s *MemoryStore) GetSession(ctx context.Context, customerID string, tokenHash string) (Session, bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	// TTL expiry is lazy (DynamoDB parity): expired rows are still returned
	// and callers must re-check ExpiresAt.
	session, found := s.sessions[customerID][tokenHash]
	return session, found, nil
}

func (s *MemoryStore) DeleteSession(ctx context.Context, customerID string, tokenHash string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions[customerID], tokenHash)
	return nil
}

func (s *MemoryStore) DeleteAllSessions(ctx context.Context, customerID string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, customerID)
	return nil
}

func (s *MemoryStore) GetCart(ctx context.Context, customerID string) (CartRecord, bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	record, found := s.carts[customerID]
	if !found {
		return CartRecord{}, false, nil
	}
	return cloneCartRecord(record), true, nil
}

func (s *MemoryStore) PutCart(ctx context.Context, c CartRecord) (CartRecord, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, found := s.carts[c.CustomerID]; found && existing.Version != c.Version {
		return CartRecord{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	record := cloneCartRecord(c)
	record.Version = c.Version + 1
	record.UpdatedAt = s.clock()
	s.carts[c.CustomerID] = record
	return cloneCartRecord(record), nil
}

func (s *MemoryStore) CreateAddress(ctx context.Context, a Address) (Address, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.addresses[a.CustomerID]) >= MaxAddressesPerCustomer {
		return Address{}, fmt.Errorf("%w", ErrAddressLimit)
	}
	if a.ID == "" {
		a.ID = s.generateID()
	}
	if _, exists := s.addresses[a.CustomerID][a.ID]; exists {
		return Address{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	now := s.clock()
	a.Version = 1
	a.CreatedAt = now
	a.UpdatedAt = now
	if s.addresses[a.CustomerID] == nil {
		s.addresses[a.CustomerID] = map[string]Address{}
	}
	s.addresses[a.CustomerID][a.ID] = a
	return a, nil
}

func (s *MemoryStore) UpdateAddress(ctx context.Context, a Address, expectedVersion int) (Address, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, found := s.addresses[a.CustomerID][a.ID]
	if !found || existing.Version != expectedVersion {
		return Address{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	a.Version = expectedVersion + 1
	a.CreatedAt = existing.CreatedAt
	a.UpdatedAt = s.clock()
	s.addresses[a.CustomerID][a.ID] = a
	return a, nil
}

func (s *MemoryStore) DeleteAddress(ctx context.Context, customerID string, addressID string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.addresses[customerID], addressID)
	return nil
}

func (s *MemoryStore) GetAddress(ctx context.Context, customerID string, addressID string) (Address, bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	address, found := s.addresses[customerID][addressID]
	return address, found, nil
}

func (s *MemoryStore) ListAddresses(ctx context.Context, customerID string) ([]Address, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	addresses := make([]Address, 0, len(s.addresses[customerID]))
	for _, address := range s.addresses[customerID] {
		addresses = append(addresses, address)
	}
	// Table-equivalent sort order: ADDRESS#<id> sk ascending.
	sort.Slice(addresses, func(i int, j int) bool {
		return addresses[i].ID < addresses[j].ID
	})
	return addresses, nil
}

func (s *MemoryStore) CreateOrder(ctx context.Context, o Order) (Order, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	if o.ID == "" {
		o.ID = s.generateID()
	}
	if _, exists := s.orders[o.ID]; exists {
		return Order{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	now := s.clock()
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now
	}
	if o.UpdatedAt.IsZero() {
		o.UpdatedAt = o.CreatedAt
	}
	o.Version = 1
	if len(o.StatusHistory) == 0 {
		o.StatusHistory = []StatusEvent{{Status: o.Status, At: o.CreatedAt, Actor: OrderActorCustomer}}
	}
	s.orders[o.ID] = cloneOrder(o)
	return cloneOrder(o), nil
}

func (s *MemoryStore) GetOrder(ctx context.Context, orderID string) (Order, bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	order, found := s.orders[orderID]
	if !found {
		return Order{}, false, nil
	}
	return cloneOrder(order), true, nil
}

func (s *MemoryStore) ListOrdersByCustomer(ctx context.Context, customerID string, limit int, cursor OrderCursor) (OrderPage, error) {
	if customerID == "" {
		// Guest marker: without this guard the filter below would match every
		// guest order ("" == ""), turning guests into a pseudo-customer.
		return OrderPage{}, nil
	}
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	orders := make([]Order, 0)
	for _, order := range s.orders {
		if order.CustomerID == customerID {
			orders = append(orders, cloneOrder(order))
		}
	}
	sortOrdersNewestFirst(orders)
	return orderPageFromProbe(ordersAfterCursor(orders, cursor), limit), nil
}

func (s *MemoryStore) ListOrders(ctx context.Context, limit int, cursor OrderCursor) (OrderPage, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	orders := make([]Order, 0, len(s.orders))
	for _, order := range s.orders {
		orders = append(orders, cloneOrder(order))
	}
	sortOrdersNewestFirst(orders)
	return orderPageFromProbe(ordersAfterCursor(orders, cursor), limit), nil
}

func (s *MemoryStore) TransitionOrder(ctx context.Context, orderID string, from OrderStatus, to OrderStatus, patch OrderPatch) (Order, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	order, found := s.orders[orderID]
	if !found {
		return Order{}, fmt.Errorf("commerce order %q not found", orderID)
	}
	if order.Status == to {
		// Replayed transition: no-op success.
		return cloneOrder(order), nil
	}
	if !AllowedOrderTransition(from, to) {
		return Order{}, fmt.Errorf("%w: %s to %s is not allowed", ErrOrderTransitionConflict, from, to)
	}
	if order.Status != from {
		return Order{}, fmt.Errorf("%w: order is %s, not %s", ErrOrderTransitionConflict, order.Status, from)
	}

	now := s.clock()
	order = cloneOrder(order)
	order.Status = to
	order.Version++
	order.UpdatedAt = now
	order.StatusHistory = append(order.StatusHistory, StatusEvent{Status: to, At: now, Actor: orderActorOrDefault(patch.Actor)})
	applyOrderPatchFields(&order, patch)
	s.orders[orderID] = cloneOrder(order)
	return order, nil
}

func (s *MemoryStore) PatchOrder(ctx context.Context, orderID string, expectedStatus OrderStatus, expectedVersion int, patch OrderPatch) (Order, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	order, found := s.orders[orderID]
	if !found || order.Status != expectedStatus || order.Version != expectedVersion {
		return Order{}, fmt.Errorf("%w", ErrVersionConflict)
	}
	order = cloneOrder(order)
	order.Version = expectedVersion + 1
	order.UpdatedAt = s.clock()
	applyOrderPatchFields(&order, patch)
	s.orders[orderID] = cloneOrder(order)
	return order, nil
}

func (s *MemoryStore) MarkStripeEventProcessed(ctx context.Context, eventID string, eventType string, orderID string) (bool, error) {
	_ = ctx
	_ = eventType
	_ = orderID
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stripeEvents[eventID] {
		return true, nil
	}
	s.stripeEvents[eventID] = true
	return false, nil
}

func cloneCartRecord(record CartRecord) CartRecord {
	record.Lines = slices.Clone(record.Lines)
	return record
}

func cloneOrder(order Order) Order {
	order.Lines = slices.Clone(order.Lines)
	order.StatusHistory = slices.Clone(order.StatusHistory)
	return order
}

// sortOrdersNewestFirst mirrors the orders-index sort key
// "<created_at RFC3339>#<orderID>" descending.
func sortOrdersNewestFirst(orders []Order) {
	sort.SliceStable(orders, func(i int, j int) bool {
		return ordersIndexSK(orders[i]) > ordersIndexSK(orders[j])
	})
}

// ordersAfterCursor mirrors DynamoDB ExclusiveStartKey semantics positionally:
// skip every order whose newest-first sort key is at or before the cursor's.
// Comparison runs on the formatted index sort key (second-precision RFC3339 +
// order-ID tiebreak), exactly like the GSI, so nanosecond CreatedAt values in
// memory cannot diverge from Dynamo behavior.
func ordersAfterCursor(orders []Order, cursor OrderCursor) []Order {
	if cursor.IsZero() {
		return orders
	}
	startSK := ordersIndexSK(Order{ID: cursor.OrderID, CreatedAt: cursor.CreatedAt})
	for index, order := range orders {
		if ordersIndexSK(order) < startSK {
			return orders[index:]
		}
	}
	return nil
}
