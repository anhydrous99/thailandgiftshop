package commerce

import (
	"context"
	"testing"
	"time"
)

func testPasswordResetToken(customerID string, tokenHash string, now time.Time) PasswordResetToken {
	return PasswordResetToken{
		CustomerID: customerID,
		TokenHash:  tokenHash,
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Hour),
		Version:    1,
	}
}

func TestPasswordResetTokenReissueOverwritesPerCustomer(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")

			first := testPasswordResetToken(customer.ID, "first-token-hash", fixture.clock.Now())
			if err := fixture.store.PutPasswordResetToken(ctx, first); err != nil {
				t.Fatalf("PutPasswordResetToken first returned error: %v", err)
			}

			fixture.clock.Advance(time.Minute)
			second := testPasswordResetToken(customer.ID, "second-token-hash", fixture.clock.Now())
			if err := fixture.store.PutPasswordResetToken(ctx, second); err != nil {
				t.Fatalf("PutPasswordResetToken second returned error: %v", err)
			}

			if _, found, err := fixture.store.ValidatePasswordResetToken(ctx, customer.ID, first.TokenHash, fixture.clock.Now()); err != nil || found {
				t.Fatalf("old token ValidatePasswordResetToken found=%v err=%v, want overwritten miss", found, err)
			}
			stored, found, err := fixture.store.ValidatePasswordResetToken(ctx, customer.ID, second.TokenHash, fixture.clock.Now())
			if err != nil || !found {
				t.Fatalf("new token ValidatePasswordResetToken found=%v err=%v, want found", found, err)
			}
			if stored.TokenHash != second.TokenHash || !stored.CreatedAt.Equal(second.CreatedAt) || !stored.ExpiresAt.Equal(second.ExpiresAt) {
				t.Fatalf("stored reset token = %#v, want second token", stored)
			}
		})
	}
}

func TestPasswordResetTokenValidateIsNonConsuming(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			token := testPasswordResetToken(customer.ID, "reset-token-hash", fixture.clock.Now())
			if err := fixture.store.PutPasswordResetToken(ctx, token); err != nil {
				t.Fatalf("PutPasswordResetToken returned error: %v", err)
			}

			for attempt := range 2 {
				stored, found, err := fixture.store.ValidatePasswordResetToken(ctx, customer.ID, token.TokenHash, fixture.clock.Now())
				if err != nil || !found {
					t.Fatalf("ValidatePasswordResetToken attempt %d found=%v err=%v, want found", attempt+1, found, err)
				}
				if !stored.UsedAt.IsZero() {
					t.Fatalf("ValidatePasswordResetToken marked UsedAt = %s, want zero", stored.UsedAt)
				}
			}

			consumed, found, err := fixture.store.ConsumePasswordResetToken(ctx, customer.ID, token.TokenHash, fixture.clock.Now())
			if err != nil || !found {
				t.Fatalf("ConsumePasswordResetToken after validation found=%v err=%v, want found", found, err)
			}
			if consumed.UsedAt.IsZero() {
				t.Fatalf("ConsumePasswordResetToken UsedAt is zero, want consumption timestamp")
			}
		})
	}
}

func TestPasswordResetTokenConsumeSingleUseAndFailures(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			token := testPasswordResetToken(customer.ID, "reset-token-hash", fixture.clock.Now())
			if err := fixture.store.PutPasswordResetToken(ctx, token); err != nil {
				t.Fatalf("PutPasswordResetToken returned error: %v", err)
			}

			if _, found, err := fixture.store.ConsumePasswordResetToken(ctx, customer.ID, "wrong-token-hash", fixture.clock.Now()); err != nil || found {
				t.Fatalf("mismatched ConsumePasswordResetToken found=%v err=%v, want miss", found, err)
			}
			if _, found, err := fixture.store.ConsumePasswordResetToken(ctx, "missingcustomer", token.TokenHash, fixture.clock.Now()); err != nil || found {
				t.Fatalf("missing ConsumePasswordResetToken found=%v err=%v, want miss", found, err)
			}

			consumed, found, err := fixture.store.ConsumePasswordResetToken(ctx, customer.ID, token.TokenHash, fixture.clock.Now())
			if err != nil || !found {
				t.Fatalf("matching ConsumePasswordResetToken found=%v err=%v, want found", found, err)
			}
			if consumed.UsedAt.IsZero() || consumed.Version != token.Version+1 {
				t.Fatalf("consumed token = %#v, want UsedAt and version bump", consumed)
			}
			if _, found, err := fixture.store.ConsumePasswordResetToken(ctx, customer.ID, token.TokenHash, fixture.clock.Now()); err != nil || found {
				t.Fatalf("already-used ConsumePasswordResetToken found=%v err=%v, want miss", found, err)
			}
		})
	}
}

func TestPasswordResetTokenExpiredRejected(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			token := testPasswordResetToken(customer.ID, "reset-token-hash", fixture.clock.Now())
			if err := fixture.store.PutPasswordResetToken(ctx, token); err != nil {
				t.Fatalf("PutPasswordResetToken returned error: %v", err)
			}

			expiredAt := token.ExpiresAt.Add(time.Nanosecond)
			if _, found, err := fixture.store.ValidatePasswordResetToken(ctx, customer.ID, token.TokenHash, expiredAt); err != nil || found {
				t.Fatalf("expired ValidatePasswordResetToken found=%v err=%v, want miss", found, err)
			}
			if _, found, err := fixture.store.ConsumePasswordResetToken(ctx, customer.ID, token.TokenHash, expiredAt); err != nil || found {
				t.Fatalf("expired ConsumePasswordResetToken found=%v err=%v, want miss", found, err)
			}
		})
	}
}

func TestPasswordResetTokenDelete(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "shopper@example.test")
			token := testPasswordResetToken(customer.ID, "reset-token-hash", fixture.clock.Now())
			if err := fixture.store.PutPasswordResetToken(ctx, token); err != nil {
				t.Fatalf("PutPasswordResetToken returned error: %v", err)
			}
			if err := fixture.store.DeletePasswordResetToken(ctx, customer.ID); err != nil {
				t.Fatalf("DeletePasswordResetToken returned error: %v", err)
			}
			if _, found, err := fixture.store.ValidatePasswordResetToken(ctx, customer.ID, token.TokenHash, fixture.clock.Now()); err != nil || found {
				t.Fatalf("ValidatePasswordResetToken after delete found=%v err=%v, want miss", found, err)
			}
		})
	}
}

func TestDynamoPasswordResetTokenItemShape(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	token := testPasswordResetToken("cust1", "reset-token-hash", now)
	attrs, err := passwordResetTokenItem(token)
	if err != nil {
		t.Fatalf("passwordResetTokenItem returned error: %v", err)
	}

	for _, required := range []string{"pk", "sk", "entity_type", "customer_id", "token_hash", "expires_at", "created_at", "version"} {
		if _, found := attrs[required]; !found {
			t.Fatalf("password reset token item is missing %q: %#v", required, attrs)
		}
	}
	for _, omitted := range []string{"raw_token", "token", "used_at", "gsi1pk", "gsi1sk", "gsi2pk", "gsi2sk"} {
		if _, found := attrs[omitted]; found {
			t.Fatalf("password reset token item includes %q", omitted)
		}
	}
	if got := stringAttribute(attrs, "pk"); got != "CUSTOMER#cust1" {
		t.Fatalf("pk = %q, want CUSTOMER#cust1", got)
	}
	if got := stringAttribute(attrs, "sk"); got != "PASSWORD_RESET" {
		t.Fatalf("sk = %q, want PASSWORD_RESET", got)
	}
	if got := stringAttribute(attrs, "entity_type"); got != "PASSWORD_RESET" {
		t.Fatalf("entity_type = %q, want PASSWORD_RESET", got)
	}
	if got := stringAttribute(attrs, "token_hash"); got != "reset-token-hash" {
		t.Fatalf("token_hash = %q, want persisted hash", got)
	}
	if expiresAt := numberAttribute(attrs, "expires_at"); expiresAt != token.ExpiresAt.Unix() {
		t.Fatalf("expires_at = %d, want %d", expiresAt, token.ExpiresAt.Unix())
	}

	used := token
	used.UsedAt = now.Add(5 * time.Minute)
	used.Version = 2
	usedAttrs, err := passwordResetTokenItem(used)
	if err != nil {
		t.Fatalf("passwordResetTokenItem used returned error: %v", err)
	}
	if got := stringAttribute(usedAttrs, "used_at"); got != "2026-06-08T12:05:00Z" {
		t.Fatalf("used_at = %q, want formatted timestamp", got)
	}
	if version := numberAttribute(usedAttrs, "version"); version != 2 {
		t.Fatalf("version = %d, want 2", version)
	}
}

func TestDynamoPutPasswordResetTokenWritesSingleCustomerRow(t *testing.T) {
	ctx := context.Background()
	client := newFakeCommerceClient()
	clock := newTestClock()
	store := NewDynamoStoreWithClock(client, testDynamoConfig(), nil, clock.Now)
	customer := createTestCustomer(t, store, "shopper@example.test")
	token := testPasswordResetToken(customer.ID, "reset-token-hash", clock.Now())

	if err := store.PutPasswordResetToken(ctx, token); err != nil {
		t.Fatalf("PutPasswordResetToken returned error: %v", err)
	}
	item := client.itemForKey(t, customerPK(customer.ID), passwordResetSK)
	if got := stringAttribute(item, "token_hash"); got != token.TokenHash {
		t.Fatalf("token_hash = %q, want %q", got, token.TokenHash)
	}
	for _, gsiAttribute := range []string{"gsi1pk", "gsi1sk", "gsi2pk", "gsi2sk"} {
		if _, found := item[gsiAttribute]; found {
			t.Fatalf("password reset row carries %s; reset rows must have no GSI attributes", gsiAttribute)
		}
	}
	for _, rawAttribute := range []string{"raw_token", "token"} {
		if _, found := item[rawAttribute]; found {
			t.Fatalf("password reset row carries raw token attribute %s", rawAttribute)
		}
	}

	clock.Advance(time.Minute)
	reissued := testPasswordResetToken(customer.ID, "replacement-token-hash", clock.Now())
	if err := store.PutPasswordResetToken(ctx, reissued); err != nil {
		t.Fatalf("reissue PutPasswordResetToken returned error: %v", err)
	}
	item = client.itemForKey(t, customerPK(customer.ID), passwordResetSK)
	if got := stringAttribute(item, "token_hash"); got != reissued.TokenHash {
		t.Fatalf("reissued token_hash = %q, want %q", got, reissued.TokenHash)
	}
	if _, found := item["used_at"]; found {
		t.Fatalf("reissued password reset row retained used_at: %#v", item["used_at"])
	}
}
