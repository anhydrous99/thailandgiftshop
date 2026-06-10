package commerce

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/cart"
)

// testPasswordHash is a bcrypt cost-4 fixture; the store treats hashes as
// opaque strings.
const testPasswordHash = "$2a$04$/TBToL45Vw2LPUN/Ib/YWOWGa05betZNEKcrOYyyRyhXcrVqZR8Tu"

var testBaseTime = time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

var idPattern = regexp.MustCompile(`^[a-z0-9]{26}$`)

type testClock struct {
	mu      sync.Mutex
	current time.Time
}

func newTestClock() *testClock {
	return &testClock{current: testBaseTime}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

type storeFixture struct {
	name  string
	store Store
	clock *testClock
}

// storeFixtures returns the memory store and the Dynamo store backed by the
// fake client so every behavior is asserted identically against both
// implementations.
func storeFixtures(t *testing.T) []storeFixture {
	t.Helper()
	memoryClock := newTestClock()
	dynamoClock := newTestClock()
	return []storeFixture{
		{name: "memory", store: NewMemoryStoreWithClock(memoryClock.Now), clock: memoryClock},
		{name: "dynamo", store: NewDynamoStoreWithClock(newFakeCommerceClient(), testDynamoConfig(), nil, dynamoClock.Now), clock: dynamoClock},
	}
}

func createTestCustomer(t *testing.T, store Store, email string) Customer {
	t.Helper()
	customer, err := store.CreateCustomer(context.Background(), email, NormalizeEmail(email), testPasswordHash)
	if err != nil {
		t.Fatalf("CreateCustomer(%q) returned error: %v", email, err)
	}
	return customer
}

func TestCreateCustomerAndLookups(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "Shopper@Example.test")

			if !idPattern.MatchString(customer.ID) {
				t.Fatalf("customer ID = %q, want 26-char lowercase id", customer.ID)
			}
			if customer.Email != "Shopper@Example.test" || customer.EmailNormalized != "shopper@example.test" {
				t.Fatalf("customer = %#v, want display email preserved and normalized email stored", customer)
			}
			if customer.PasswordHash != testPasswordHash || customer.Version != 1 || customer.EmailVerified {
				t.Fatalf("customer = %#v, want hash, version 1, unverified email", customer)
			}
			if !customer.CreatedAt.Equal(testBaseTime) || !customer.UpdatedAt.Equal(testBaseTime) {
				t.Fatalf("customer timestamps = %s / %s, want %s", customer.CreatedAt, customer.UpdatedAt, testBaseTime)
			}

			byEmail, found, err := fixture.store.GetCustomerByEmail(ctx, "shopper@example.test")
			if err != nil || !found || byEmail.ID != customer.ID {
				t.Fatalf("GetCustomerByEmail = %#v %v %v, want stored customer", byEmail, found, err)
			}
			byID, found, err := fixture.store.GetCustomerByID(ctx, customer.ID)
			if err != nil || !found || byID.EmailNormalized != "shopper@example.test" {
				t.Fatalf("GetCustomerByID = %#v %v %v, want stored customer", byID, found, err)
			}

			if _, found, err := fixture.store.GetCustomerByEmail(ctx, "missing@example.test"); err != nil || found {
				t.Fatalf("GetCustomerByEmail miss = %v %v, want not found", found, err)
			}
			if _, found, err := fixture.store.GetCustomerByID(ctx, "missingcustomerid"); err != nil || found {
				t.Fatalf("GetCustomerByID miss = %v %v, want not found", found, err)
			}
		})
	}
}

func TestCreateCustomerRejectsDuplicateEmail(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			createTestCustomer(t, fixture.store, "shopper@example.test")

			_, err := fixture.store.CreateCustomer(context.Background(), "SHOPPER@example.test", "shopper@example.test", testPasswordHash)
			if !errors.Is(err, ErrEmailTaken) {
				t.Fatalf("duplicate CreateCustomer error = %v, want %v", err, ErrEmailTaken)
			}
		})
	}
}

func TestSetStripeCustomerIDFirstWriteWinsRace(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			first, err := fixture.store.SetStripeCustomerID(ctx, customer.ID, "cus_first")
			if err != nil || first.StripeCustomerID != "cus_first" {
				t.Fatalf("SetStripeCustomerID = %#v %v, want cus_first", first, err)
			}

			second, err := fixture.store.SetStripeCustomerID(ctx, customer.ID, "cus_second")
			if err != nil {
				t.Fatalf("racing SetStripeCustomerID returned error: %v", err)
			}
			if second.StripeCustomerID != "cus_first" {
				t.Fatalf("racing SetStripeCustomerID = %q, want existing winner cus_first", second.StripeCustomerID)
			}

			if _, err := fixture.store.SetStripeCustomerID(ctx, "missingcustomerid", "cus_x"); err == nil {
				t.Fatalf("SetStripeCustomerID for missing customer succeeded, want error")
			}
		})
	}
}

func TestSetDefaultAddressVersioned(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			if err := fixture.store.SetDefaultAddress(ctx, customer.ID, "addr123", customer.Version+5); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale SetDefaultAddress error = %v, want %v", err, ErrVersionConflict)
			}
			if err := fixture.store.SetDefaultAddress(ctx, customer.ID, "addr123", customer.Version); err != nil {
				t.Fatalf("SetDefaultAddress returned error: %v", err)
			}

			updated, _, err := fixture.store.GetCustomerByID(ctx, customer.ID)
			if err != nil || updated.DefaultAddressID != "addr123" || updated.Version != customer.Version+1 {
				t.Fatalf("customer after SetDefaultAddress = %#v %v, want addr123 at version %d", updated, err, customer.Version+1)
			}

			// Clearing the default removes the pointer.
			if err := fixture.store.SetDefaultAddress(ctx, customer.ID, "", updated.Version); err != nil {
				t.Fatalf("clearing SetDefaultAddress returned error: %v", err)
			}
			cleared, _, err := fixture.store.GetCustomerByID(ctx, customer.ID)
			if err != nil || cleared.DefaultAddressID != "" || cleared.Version != updated.Version+1 {
				t.Fatalf("customer after clearing default = %#v %v, want empty default", cleared, err)
			}
		})
	}
}

func TestUpdatePasswordVersionedAndConflicts(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			if err := fixture.store.UpdatePassword(ctx, customer.ID, "$2a$04$newhashnewhashnewhashnew", customer.Version+3); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale UpdatePassword error = %v, want %v", err, ErrVersionConflict)
			}
			if err := fixture.store.UpdatePassword(ctx, customer.ID, "$2a$04$newhashnewhashnewhashnew", customer.Version); err != nil {
				t.Fatalf("UpdatePassword returned error: %v", err)
			}

			updated, _, err := fixture.store.GetCustomerByID(ctx, customer.ID)
			if err != nil || updated.PasswordHash != "$2a$04$newhashnewhashnewhashnew" || updated.Version != customer.Version+1 {
				t.Fatalf("customer after UpdatePassword = %#v %v", updated, err)
			}
		})
	}
}

func TestSessionLifecycleAndLazyTTL(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			session := Session{
				CustomerID: customer.ID,
				TokenHash:  "aaaa1111bbbb2222cccc3333dddd4444",
				Nonce:      "nonce-value",
				CreatedAt:  fixture.clock.Now(),
				ExpiresAt:  fixture.clock.Now().Add(30 * 24 * time.Hour),
			}
			if err := fixture.store.PutSession(ctx, session); err != nil {
				t.Fatalf("PutSession returned error: %v", err)
			}

			stored, found, err := fixture.store.GetSession(ctx, customer.ID, session.TokenHash)
			if err != nil || !found {
				t.Fatalf("GetSession = %v %v, want found", found, err)
			}
			if stored.Nonce != "nonce-value" || !stored.ExpiresAt.Equal(session.ExpiresAt) || stored.CustomerID != customer.ID {
				t.Fatalf("stored session = %#v, want round-tripped fields", stored)
			}

			// TTL deletion is lazy: an expired row is still returned and the
			// caller's clock check is what retires it.
			fixture.clock.Advance(31 * 24 * time.Hour)
			expired, found, err := fixture.store.GetSession(ctx, customer.ID, session.TokenHash)
			if err != nil || !found {
				t.Fatalf("expired GetSession = %v %v, want lazily-retained row", found, err)
			}
			if !expired.ExpiresAt.Before(fixture.clock.Now()) {
				t.Fatalf("expired session ExpiresAt = %s, want before %s so callers treat it as anonymous", expired.ExpiresAt, fixture.clock.Now())
			}

			if err := fixture.store.DeleteSession(ctx, customer.ID, session.TokenHash); err != nil {
				t.Fatalf("DeleteSession returned error: %v", err)
			}
			if _, found, err := fixture.store.GetSession(ctx, customer.ID, session.TokenHash); err != nil || found {
				t.Fatalf("GetSession after delete = %v %v, want gone", found, err)
			}
		})
	}
}

func TestDeleteAllSessionsLeavesOtherRowsIntact(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			other := createTestCustomer(t, fixture.store, "other@example.test")
			expiry := fixture.clock.Now().Add(30 * 24 * time.Hour)

			for _, tokenHash := range []string{"hash-one", "hash-two", "hash-three"} {
				if err := fixture.store.PutSession(ctx, Session{CustomerID: customer.ID, TokenHash: tokenHash, Nonce: "n", CreatedAt: fixture.clock.Now(), ExpiresAt: expiry}); err != nil {
					t.Fatalf("PutSession(%q) returned error: %v", tokenHash, err)
				}
			}
			if err := fixture.store.PutSession(ctx, Session{CustomerID: other.ID, TokenHash: "other-hash", Nonce: "n", CreatedAt: fixture.clock.Now(), ExpiresAt: expiry}); err != nil {
				t.Fatalf("PutSession for other customer returned error: %v", err)
			}
			if _, err := fixture.store.PutCart(ctx, CartRecord{CustomerID: customer.ID, Lines: []cart.Line{{Slug: "mango-sticky-rice-kit", Quantity: 1}}}); err != nil {
				t.Fatalf("PutCart returned error: %v", err)
			}

			if err := fixture.store.DeleteAllSessions(ctx, customer.ID); err != nil {
				t.Fatalf("DeleteAllSessions returned error: %v", err)
			}

			for _, tokenHash := range []string{"hash-one", "hash-two", "hash-three"} {
				if _, found, err := fixture.store.GetSession(ctx, customer.ID, tokenHash); err != nil || found {
					t.Fatalf("session %q survived DeleteAllSessions: found=%v err=%v", tokenHash, found, err)
				}
			}
			if _, found, err := fixture.store.GetSession(ctx, other.ID, "other-hash"); err != nil || !found {
				t.Fatalf("other customer's session was deleted: found=%v err=%v", found, err)
			}
			if _, found, err := fixture.store.GetCart(ctx, customer.ID); err != nil || !found {
				t.Fatalf("cart row was deleted by DeleteAllSessions: found=%v err=%v", found, err)
			}
			if _, found, err := fixture.store.GetCustomerByID(ctx, customer.ID); err != nil || !found {
				t.Fatalf("customer row was deleted by DeleteAllSessions: found=%v err=%v", found, err)
			}
		})
	}
}

func TestPutCartVersioning(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			if _, found, err := fixture.store.GetCart(ctx, customer.ID); err != nil || found {
				t.Fatalf("GetCart before write = %v %v, want not found", found, err)
			}

			lines := []cart.Line{
				{Slug: "thai-tea-blend", Quantity: 2},
				{Slug: "celadon-bowl", VariantID: "large", Quantity: 1},
			}
			written, err := fixture.store.PutCart(ctx, CartRecord{CustomerID: customer.ID, Lines: lines, PendingOrderID: "order123", PendingFingerprint: "fp123"})
			if err != nil {
				t.Fatalf("PutCart returned error: %v", err)
			}
			if written.Version != 1 {
				t.Fatalf("written cart version = %d, want 1", written.Version)
			}

			stored, found, err := fixture.store.GetCart(ctx, customer.ID)
			if err != nil || !found {
				t.Fatalf("GetCart = %v %v, want found", found, err)
			}
			if len(stored.Lines) != 2 || stored.Lines[0] != lines[0] || stored.Lines[1] != lines[1] {
				t.Fatalf("stored cart lines = %#v, want %#v", stored.Lines, lines)
			}
			if stored.PendingOrderID != "order123" || stored.PendingFingerprint != "fp123" || stored.Version != 1 {
				t.Fatalf("stored cart = %#v, want pending pointers and version 1", stored)
			}

			// Stale version is rejected.
			if _, err := fixture.store.PutCart(ctx, CartRecord{CustomerID: customer.ID, Lines: lines, Version: 0}); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale PutCart error = %v, want %v", err, ErrVersionConflict)
			}

			// Read-modify-write with the current version succeeds and clears
			// pending pointers.
			updated, err := fixture.store.PutCart(ctx, CartRecord{CustomerID: customer.ID, Lines: lines[:1], Version: stored.Version})
			if err != nil || updated.Version != 2 {
				t.Fatalf("PutCart update = %#v %v, want version 2", updated, err)
			}
			final, _, err := fixture.store.GetCart(ctx, customer.ID)
			if err != nil || len(final.Lines) != 1 || final.PendingOrderID != "" || final.PendingFingerprint != "" {
				t.Fatalf("final cart = %#v %v, want one line and cleared pending pointers", final, err)
			}
		})
	}
}

func testAddress(customerID string, fullName string) Address {
	return Address{
		CustomerID: customerID,
		FullName:   fullName,
		Line1:      "1 Sukhumvit Road",
		Line2:      "Apt 2",
		City:       "Austin",
		Region:     "TX",
		PostalCode: "78701",
		Country:    "US",
		Phone:      "+1 512 555 0100",
	}
}

func TestAddressLifecycle(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			created, err := fixture.store.CreateAddress(ctx, testAddress(customer.ID, "A Shopper"))
			if err != nil {
				t.Fatalf("CreateAddress returned error: %v", err)
			}
			if !idPattern.MatchString(created.ID) || created.Version != 1 {
				t.Fatalf("created address = %#v, want generated id and version 1", created)
			}
			if !created.CreatedAt.Equal(testBaseTime) {
				t.Fatalf("created.CreatedAt = %s, want %s", created.CreatedAt, testBaseTime)
			}

			fetched, found, err := fixture.store.GetAddress(ctx, customer.ID, created.ID)
			if err != nil || !found || fetched.FullName != "A Shopper" || fetched.Line2 != "Apt 2" {
				t.Fatalf("GetAddress = %#v %v %v", fetched, found, err)
			}

			// Stale version is rejected.
			edit := fetched
			edit.FullName = "Renamed Shopper"
			if _, err := fixture.store.UpdateAddress(ctx, edit, fetched.Version+4); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale UpdateAddress error = %v, want %v", err, ErrVersionConflict)
			}

			fixture.clock.Advance(time.Minute)
			edit.Line2 = ""
			edit.Phone = ""
			updated, err := fixture.store.UpdateAddress(ctx, edit, fetched.Version)
			if err != nil {
				t.Fatalf("UpdateAddress returned error: %v", err)
			}
			if updated.Version != fetched.Version+1 || updated.FullName != "Renamed Shopper" || updated.Line2 != "" || updated.Phone != "" {
				t.Fatalf("updated address = %#v, want version bump and cleared optional fields", updated)
			}
			if !updated.CreatedAt.Equal(created.CreatedAt) {
				t.Fatalf("updated.CreatedAt = %s, want preserved %s", updated.CreatedAt, created.CreatedAt)
			}

			if err := fixture.store.DeleteAddress(ctx, customer.ID, created.ID); err != nil {
				t.Fatalf("DeleteAddress returned error: %v", err)
			}
			if _, found, err := fixture.store.GetAddress(ctx, customer.ID, created.ID); err != nil || found {
				t.Fatalf("GetAddress after delete = %v %v, want gone", found, err)
			}

			if _, err := fixture.store.UpdateAddress(ctx, edit, updated.Version); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("UpdateAddress on missing address error = %v, want %v", err, ErrVersionConflict)
			}
		})
	}
}

func TestCreateAddressEnforcesCap(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			for i := 0; i < MaxAddressesPerCustomer; i++ {
				if _, err := fixture.store.CreateAddress(ctx, testAddress(customer.ID, "Shopper")); err != nil {
					t.Fatalf("CreateAddress %d returned error: %v", i+1, err)
				}
			}
			if _, err := fixture.store.CreateAddress(ctx, testAddress(customer.ID, "One Too Many")); !errors.Is(err, ErrAddressLimit) {
				t.Fatalf("CreateAddress over cap error = %v, want %v", err, ErrAddressLimit)
			}

			addresses, err := fixture.store.ListAddresses(ctx, customer.ID)
			if err != nil || len(addresses) != MaxAddressesPerCustomer {
				t.Fatalf("ListAddresses = %d addresses, %v; want %d", len(addresses), err, MaxAddressesPerCustomer)
			}
			for i := 1; i < len(addresses); i++ {
				if addresses[i-1].ID > addresses[i].ID {
					t.Fatalf("ListAddresses not sorted by id: %q before %q", addresses[i-1].ID, addresses[i].ID)
				}
			}

			// The cap is per customer, not global.
			other := createTestCustomer(t, fixture.store, "other@example.test")
			if _, err := fixture.store.CreateAddress(ctx, testAddress(other.ID, "Other Shopper")); err != nil {
				t.Fatalf("CreateAddress for other customer returned error: %v", err)
			}
		})
	}
}

func testOrder(customerID string, totalCents int) Order {
	return Order{
		CustomerID: customerID,
		Email:      "shopper@example.test",
		Status:     OrderStatusPendingPayment,
		Lines: []OrderLine{{
			Slug:           "thai-tea-blend",
			ProductID:      "prod1",
			Name:           "Thai Tea Blend",
			VariantID:      "large",
			VariantLabel:   "Large",
			UnitPriceCents: totalCents,
			Quantity:       1,
			LineTotalCents: totalCents,
			ImageURL:       "/images/thai-tea-blend.jpg",
		}},
		SubtotalCents: totalCents,
		ShippingCents: 0,
		TaxCents:      0,
		TotalCents:    totalCents,
		Currency:      "usd",
		ShippingAddress: OrderAddress{
			FullName:   "A Shopper",
			Line1:      "1 Sukhumvit Road",
			City:       "Austin",
			Region:     "TX",
			PostalCode: "78701",
			Country:    "US",
		},
		CartFingerprint: "fp123",
	}
}

func TestCreateOrderSeedsHistoryAndRejectsDuplicates(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			created, err := fixture.store.CreateOrder(ctx, testOrder(customer.ID, 1850))
			if err != nil {
				t.Fatalf("CreateOrder returned error: %v", err)
			}
			if !idPattern.MatchString(created.ID) || created.Version != 1 {
				t.Fatalf("created order = id %q version %d, want generated id and version 1", created.ID, created.Version)
			}
			if len(created.StatusHistory) != 1 || created.StatusHistory[0].Status != OrderStatusPendingPayment || created.StatusHistory[0].Actor != OrderActorCustomer {
				t.Fatalf("created status history = %#v, want one pending_payment customer entry", created.StatusHistory)
			}
			if !created.CreatedAt.Equal(testBaseTime) || !created.StatusHistory[0].At.Equal(testBaseTime) {
				t.Fatalf("created timestamps = %s / %s, want %s", created.CreatedAt, created.StatusHistory[0].At, testBaseTime)
			}

			stored, found, err := fixture.store.GetOrder(ctx, created.ID)
			if err != nil || !found {
				t.Fatalf("GetOrder = %v %v, want found", found, err)
			}
			if len(stored.Lines) != 1 || stored.Lines[0] != created.Lines[0] {
				t.Fatalf("stored lines = %#v, want frozen snapshot %#v", stored.Lines, created.Lines)
			}
			if stored.ShippingAddress != created.ShippingAddress || stored.TotalCents != 1850 || stored.Currency != "usd" {
				t.Fatalf("stored order = %#v, want snapshot round trip", stored)
			}
			if stored.ShippingCents != 0 || stored.TaxCents != 0 {
				t.Fatalf("stored shipping/tax = %d/%d, want persisted zeros", stored.ShippingCents, stored.TaxCents)
			}

			if _, err := fixture.store.CreateOrder(ctx, Order{ID: created.ID, CustomerID: customer.ID, Status: OrderStatusPendingPayment, Currency: "usd"}); err == nil {
				t.Fatalf("CreateOrder with duplicate id succeeded, want error")
			}

			if _, found, err := fixture.store.GetOrder(ctx, "missingorderid"); err != nil || found {
				t.Fatalf("GetOrder miss = %v %v, want not found", found, err)
			}
		})
	}
}

func TestListOrdersNewestFirstWithLimit(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			other := createTestCustomer(t, fixture.store, "other@example.test")

			var customerOrders []Order
			for i := 0; i < 3; i++ {
				order, err := fixture.store.CreateOrder(ctx, testOrder(customer.ID, 1000+i))
				if err != nil {
					t.Fatalf("CreateOrder %d returned error: %v", i, err)
				}
				customerOrders = append(customerOrders, order)
				fixture.clock.Advance(time.Hour)
			}
			otherOrder, err := fixture.store.CreateOrder(ctx, testOrder(other.ID, 500))
			if err != nil {
				t.Fatalf("CreateOrder for other customer returned error: %v", err)
			}

			listed, err := fixture.store.ListOrdersByCustomer(ctx, customer.ID, 20)
			if err != nil {
				t.Fatalf("ListOrdersByCustomer returned error: %v", err)
			}
			if len(listed) != 3 {
				t.Fatalf("ListOrdersByCustomer returned %d orders, want 3", len(listed))
			}
			for i, want := range []string{customerOrders[2].ID, customerOrders[1].ID, customerOrders[0].ID} {
				if listed[i].ID != want {
					t.Fatalf("ListOrdersByCustomer[%d] = %q, want %q (newest first)", i, listed[i].ID, want)
				}
			}

			limited, err := fixture.store.ListOrdersByCustomer(ctx, customer.ID, 2)
			if err != nil || len(limited) != 2 || limited[0].ID != customerOrders[2].ID {
				t.Fatalf("limited ListOrdersByCustomer = %d orders %v, want newest 2", len(limited), err)
			}

			all, err := fixture.store.ListOrders(ctx, 20)
			if err != nil {
				t.Fatalf("ListOrders returned error: %v", err)
			}
			if len(all) != 4 || all[0].ID != otherOrder.ID {
				t.Fatalf("ListOrders = %d orders first %q, want 4 with newest (%q) first", len(all), all[0].ID, otherOrder.ID)
			}
		})
	}
}

func TestOrderTransitionAppliesPatchAndAppendsHistory(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			order, err := fixture.store.CreateOrder(ctx, testOrder(customer.ID, 1850))
			if err != nil {
				t.Fatalf("CreateOrder returned error: %v", err)
			}

			fixture.clock.Advance(5 * time.Minute)
			paidAt := fixture.clock.Now()
			paid, err := fixture.store.TransitionOrder(ctx, order.ID, OrderStatusPendingPayment, OrderStatusPaid, OrderPatch{
				Actor:                 OrderActorStripe,
				StripePaymentIntentID: ptr("pi_123"),
				PaymentCardBrand:      ptr("visa"),
				PaymentCardLast4:      ptr("4242"),
				PaidAt:                &paidAt,
			})
			if err != nil {
				t.Fatalf("TransitionOrder to paid returned error: %v", err)
			}
			if paid.Status != OrderStatusPaid || paid.Version != order.Version+1 {
				t.Fatalf("paid order = status %s version %d, want paid version %d", paid.Status, paid.Version, order.Version+1)
			}
			if paid.StripePaymentIntentID != "pi_123" || paid.PaymentCardBrand != "visa" || paid.PaymentCardLast4 != "4242" || !paid.PaidAt.Equal(paidAt) {
				t.Fatalf("paid order patch fields = %#v", paid)
			}
			if len(paid.StatusHistory) != 2 || paid.StatusHistory[1].Status != OrderStatusPaid || paid.StatusHistory[1].Actor != OrderActorStripe || !paid.StatusHistory[1].At.Equal(paidAt) {
				t.Fatalf("paid status history = %#v, want appended paid/stripe entry", paid.StatusHistory)
			}

			// Replayed transition (duplicate webhook): no-op success, no
			// history growth, no version bump.
			replayed, err := fixture.store.TransitionOrder(ctx, order.ID, OrderStatusPendingPayment, OrderStatusPaid, OrderPatch{Actor: OrderActorStripe})
			if err != nil {
				t.Fatalf("replayed TransitionOrder returned error: %v", err)
			}
			if replayed.Version != paid.Version || len(replayed.StatusHistory) != 2 {
				t.Fatalf("replayed order = version %d history %d, want unchanged %d/2", replayed.Version, len(replayed.StatusHistory), paid.Version)
			}

			fixture.clock.Advance(time.Hour)
			shippedAt := fixture.clock.Now()
			shipped, err := fixture.store.TransitionOrder(ctx, order.ID, OrderStatusPaid, OrderStatusShipped, OrderPatch{
				Actor:           OrderActorAdmin,
				TrackingCarrier: ptr("usps"),
				TrackingNumber:  ptr("9400 1234"),
				ShippedAt:       &shippedAt,
			})
			if err != nil {
				t.Fatalf("TransitionOrder to shipped returned error: %v", err)
			}
			if shipped.TrackingCarrier != "usps" || shipped.TrackingNumber != "9400 1234" || shipped.Version != paid.Version+1 {
				t.Fatalf("shipped order = %#v", shipped)
			}

			delivered, err := fixture.store.TransitionOrder(ctx, order.ID, OrderStatusShipped, OrderStatusDelivered, OrderPatch{Actor: OrderActorAdmin})
			if err != nil {
				t.Fatalf("TransitionOrder to delivered returned error: %v", err)
			}
			if len(delivered.StatusHistory) != 4 {
				t.Fatalf("delivered history length = %d, want 4", len(delivered.StatusHistory))
			}
			wantSequence := []OrderStatus{OrderStatusPendingPayment, OrderStatusPaid, OrderStatusShipped, OrderStatusDelivered}
			for i, event := range delivered.StatusHistory {
				if event.Status != wantSequence[i] {
					t.Fatalf("history[%d] = %s, want %s", i, event.Status, wantSequence[i])
				}
			}
		})
	}
}

func TestOrderTransitionMatrix(t *testing.T) {
	allStatuses := []OrderStatus{
		OrderStatusPendingPayment, OrderStatusPaid, OrderStatusShipped,
		OrderStatusDelivered, OrderStatusPaymentFailed, OrderStatusExpired, OrderStatusCanceled,
	}
	legal := map[OrderStatus]map[OrderStatus]bool{
		OrderStatusPendingPayment: {OrderStatusPaid: true, OrderStatusPaymentFailed: true, OrderStatusExpired: true, OrderStatusCanceled: true},
		OrderStatusPaid:           {OrderStatusShipped: true, OrderStatusCanceled: true},
		OrderStatusShipped:        {OrderStatusDelivered: true},
	}

	for _, from := range allStatuses {
		for _, to := range allStatuses {
			if got, want := AllowedOrderTransition(from, to), legal[from][to]; got != want {
				t.Fatalf("AllowedOrderTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}

	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			for _, from := range allStatuses {
				for _, to := range allStatuses {
					seed := testOrder(customer.ID, 1850)
					seed.Status = from
					order, err := fixture.store.CreateOrder(ctx, seed)
					if err != nil {
						t.Fatalf("CreateOrder(%s) returned error: %v", from, err)
					}

					result, err := fixture.store.TransitionOrder(ctx, order.ID, from, to, OrderPatch{Actor: OrderActorAdmin})
					switch {
					case from == to:
						// Replay: current status already equals the target.
						if err != nil || result.Version != 1 || len(result.StatusHistory) != 1 {
							t.Fatalf("%s -> %s replay = %#v %v, want untouched no-op", from, to, result, err)
						}
					case legal[from][to]:
						if err != nil {
							t.Fatalf("legal transition %s -> %s returned error: %v", from, to, err)
						}
						if result.Status != to || result.Version != 2 {
							t.Fatalf("legal transition %s -> %s = status %s version %d", from, to, result.Status, result.Version)
						}
						if len(result.StatusHistory) != 2 || result.StatusHistory[1].Status != to {
							t.Fatalf("legal transition %s -> %s history = %#v", from, to, result.StatusHistory)
						}
					default:
						if !errors.Is(err, ErrOrderTransitionConflict) {
							t.Fatalf("illegal transition %s -> %s error = %v, want %v", from, to, err, ErrOrderTransitionConflict)
						}
						unchanged, _, getErr := fixture.store.GetOrder(ctx, order.ID)
						if getErr != nil || unchanged.Status != from || unchanged.Version != 1 {
							t.Fatalf("order mutated by illegal transition %s -> %s: %#v %v", from, to, unchanged, getErr)
						}
					}
				}
			}
		})
	}
}

func TestOrderTransitionConflictsOnWrongFromStatus(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			order, err := fixture.store.CreateOrder(ctx, testOrder(customer.ID, 1850))
			if err != nil {
				t.Fatalf("CreateOrder returned error: %v", err)
			}
			if _, err := fixture.store.TransitionOrder(ctx, order.ID, OrderStatusPendingPayment, OrderStatusPaid, OrderPatch{Actor: OrderActorStripe}); err != nil {
				t.Fatalf("TransitionOrder to paid returned error: %v", err)
			}

			// An out-of-order expired webhook can never downgrade paid.
			if _, err := fixture.store.TransitionOrder(ctx, order.ID, OrderStatusPendingPayment, OrderStatusExpired, OrderPatch{Actor: OrderActorStripe}); !errors.Is(err, ErrOrderTransitionConflict) {
				t.Fatalf("expired-after-paid error = %v, want %v", err, ErrOrderTransitionConflict)
			}
			current, _, err := fixture.store.GetOrder(ctx, order.ID)
			if err != nil || current.Status != OrderStatusPaid {
				t.Fatalf("order after rejected downgrade = %s %v, want paid", current.Status, err)
			}

			if _, err := fixture.store.TransitionOrder(ctx, "missingorderid", OrderStatusPendingPayment, OrderStatusPaid, OrderPatch{}); err == nil {
				t.Fatalf("TransitionOrder for missing order succeeded, want error")
			}
		})
	}
}

func TestPatchOrderSameStatusVersionedUpdate(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			order, err := fixture.store.CreateOrder(ctx, testOrder(customer.ID, 1850))
			if err != nil {
				t.Fatalf("CreateOrder returned error: %v", err)
			}

			patched, err := fixture.store.PatchOrder(ctx, order.ID, OrderStatusPendingPayment, order.Version, OrderPatch{
				StripeCheckoutSessionID: ptr("cs_test_123"),
				CheckoutAttempt:         ptr(1),
			})
			if err != nil {
				t.Fatalf("PatchOrder returned error: %v", err)
			}
			if patched.Status != OrderStatusPendingPayment || patched.Version != order.Version+1 {
				t.Fatalf("patched order = status %s version %d, want same status version %d", patched.Status, patched.Version, order.Version+1)
			}
			if patched.StripeCheckoutSessionID != "cs_test_123" || patched.CheckoutAttempt != 1 {
				t.Fatalf("patched fields = %#v", patched)
			}
			if len(patched.StatusHistory) != 1 {
				t.Fatalf("PatchOrder appended history: %#v", patched.StatusHistory)
			}

			// Stale version is rejected.
			if _, err := fixture.store.PatchOrder(ctx, order.ID, OrderStatusPendingPayment, order.Version, OrderPatch{StripeCheckoutSessionID: ptr("cs_other")}); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale PatchOrder error = %v, want %v", err, ErrVersionConflict)
			}
			// Wrong status is rejected.
			if _, err := fixture.store.PatchOrder(ctx, order.ID, OrderStatusPaid, patched.Version, OrderPatch{StripeCheckoutSessionID: ptr("cs_other")}); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("wrong-status PatchOrder error = %v, want %v", err, ErrVersionConflict)
			}
			// Clearing a string field works.
			cleared, err := fixture.store.PatchOrder(ctx, order.ID, OrderStatusPendingPayment, patched.Version, OrderPatch{StripeCheckoutSessionID: ptr("")})
			if err != nil || cleared.StripeCheckoutSessionID != "" {
				t.Fatalf("clearing PatchOrder = %#v %v, want empty session id", cleared, err)
			}
		})
	}
}

func TestOrderStockReleasedAtSetAndClear(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			order, err := fixture.store.CreateOrder(ctx, testOrder(customer.ID, 1850))
			if err != nil {
				t.Fatalf("CreateOrder returned error: %v", err)
			}
			if !order.StockReleasedAt.IsZero() {
				t.Fatalf("new order StockReleasedAt = %s, want zero", order.StockReleasedAt)
			}

			// The release claim is SET alongside the terminal transition.
			fixture.clock.Advance(10 * time.Minute)
			releasedAt := fixture.clock.Now()
			canceled, err := fixture.store.TransitionOrder(ctx, order.ID, OrderStatusPendingPayment, OrderStatusCanceled, OrderPatch{
				Actor:           OrderActorSystem,
				StockReleasedAt: &releasedAt,
			})
			if err != nil {
				t.Fatalf("TransitionOrder to canceled returned error: %v", err)
			}
			if !canceled.StockReleasedAt.Equal(releasedAt) {
				t.Fatalf("canceled StockReleasedAt = %s, want %s", canceled.StockReleasedAt, releasedAt)
			}
			stored, _, err := fixture.store.GetOrder(ctx, order.ID)
			if err != nil || !stored.StockReleasedAt.Equal(releasedAt) {
				t.Fatalf("stored StockReleasedAt = %s %v, want %s", stored.StockReleasedAt, err, releasedAt)
			}

			// A failed release CLEARs the claim: pointer to the zero time
			// removes the marker.
			cleared, err := fixture.store.PatchOrder(ctx, order.ID, OrderStatusCanceled, canceled.Version, OrderPatch{StockReleasedAt: &time.Time{}})
			if err != nil || !cleared.StockReleasedAt.IsZero() {
				t.Fatalf("cleared StockReleasedAt = %s %v, want zero", cleared.StockReleasedAt, err)
			}
			stored, _, err = fixture.store.GetOrder(ctx, order.ID)
			if err != nil || !stored.StockReleasedAt.IsZero() {
				t.Fatalf("stored StockReleasedAt after clear = %s %v, want zero", stored.StockReleasedAt, err)
			}

			// Re-claiming is version-conditioned: a stale version loses.
			if _, err := fixture.store.PatchOrder(ctx, order.ID, OrderStatusCanceled, canceled.Version, OrderPatch{StockReleasedAt: &releasedAt}); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale-version claim error = %v, want %v", err, ErrVersionConflict)
			}
			reclaimed, err := fixture.store.PatchOrder(ctx, order.ID, OrderStatusCanceled, cleared.Version, OrderPatch{StockReleasedAt: &releasedAt})
			if err != nil || !reclaimed.StockReleasedAt.Equal(releasedAt) {
				t.Fatalf("reclaimed StockReleasedAt = %s %v, want %s", reclaimed.StockReleasedAt, err, releasedAt)
			}

			// A nil pointer leaves the marker untouched.
			untouched, err := fixture.store.PatchOrder(ctx, order.ID, OrderStatusCanceled, reclaimed.Version, OrderPatch{TrackingNumber: ptr("9400 1234")})
			if err != nil || !untouched.StockReleasedAt.Equal(releasedAt) {
				t.Fatalf("untouched StockReleasedAt = %s %v, want %s", untouched.StockReleasedAt, err, releasedAt)
			}
		})
	}
}

func TestMarkStripeEventProcessedDeduplicates(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()

			alreadySeen, err := fixture.store.MarkStripeEventProcessed(ctx, "evt_1", "checkout.session.completed", "order123")
			if err != nil || alreadySeen {
				t.Fatalf("first MarkStripeEventProcessed = %v %v, want fresh", alreadySeen, err)
			}
			alreadySeen, err = fixture.store.MarkStripeEventProcessed(ctx, "evt_1", "checkout.session.completed", "order123")
			if err != nil || !alreadySeen {
				t.Fatalf("second MarkStripeEventProcessed = %v %v, want alreadySeen", alreadySeen, err)
			}
			alreadySeen, err = fixture.store.MarkStripeEventProcessed(ctx, "evt_2", "checkout.session.expired", "")
			if err != nil || alreadySeen {
				t.Fatalf("distinct MarkStripeEventProcessed = %v %v, want fresh", alreadySeen, err)
			}
		})
	}
}

func ptr[T any](value T) *T {
	return &value
}
