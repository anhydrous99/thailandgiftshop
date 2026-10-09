package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

func TestCredentialRevisionStoreParity(t *testing.T) {
	for _, fixture := range storeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			customer := createTestCustomer(t, fixture.store, "revision@example.test")
			if customer.CredentialRevision != 0 {
				t.Fatal("new customer revision must be zero")
			}
			if err := fixture.store.SetDefaultAddress(ctx, customer.ID, "address", customer.Version); err != nil {
				t.Fatal(err)
			}
			current, _, err := fixture.store.GetCustomerByID(ctx, customer.ID)
			if err != nil || current.CredentialRevision != 0 {
				t.Fatalf("address update changed credential revision: %v", err)
			}
			if err := fixture.store.UpdatePassword(ctx, customer.ID, "replacement-hash", customer.Version); !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("stale update = %v", err)
			}
			if err := fixture.store.UpdatePassword(ctx, customer.ID, "replacement-hash", current.Version); err != nil {
				t.Fatal(err)
			}
			current, _, err = fixture.store.GetCustomerByID(ctx, customer.ID)
			if err != nil || current.CredentialRevision != 1 || current.PasswordHash != "replacement-hash" {
				t.Fatalf("password/revision update not atomic: %v", err)
			}
			session := Session{CustomerID: customer.ID, TokenHash: "token-hash", CredentialRevision: current.CredentialRevision, ExpiresAt: testBaseTime.Add(time.Hour)}
			if err := fixture.store.PutSession(ctx, session); err != nil {
				t.Fatal(err)
			}
			stored, found, err := fixture.store.GetSession(ctx, customer.ID, session.TokenHash)
			if err != nil || !found || stored.CredentialRevision != 1 {
				t.Fatalf("session revision lost: found=%v err=%v", found, err)
			}
		})
	}
}

func TestLegacyCredentialRevisionDefaultsToZero(t *testing.T) {
	customer := Customer{ID: "legacy", CredentialRevision: 9}
	item, err := customerItem(customer)
	if err != nil {
		t.Fatal(err)
	}
	delete(item, "credential_revision")
	decoded, err := customerFromItem(item)
	if err != nil || decoded.CredentialRevision != 0 {
		t.Fatalf("legacy customer: %v", err)
	}
	sessionItemValue, err := sessionItem(Session{CustomerID: "legacy", CredentialRevision: 9})
	if err != nil {
		t.Fatal(err)
	}
	delete(sessionItemValue, "credential_revision")
	session, err := sessionFromItem(sessionItemValue)
	if err != nil || session.CredentialRevision != 0 {
		t.Fatalf("legacy session: %v", err)
	}
	// DynamoDB ADD must advance an absent legacy attribute too.
	client := newFakeCommerceClient()
	item["version"] = numberValue(1)
	client.items[fakeItemKey(item)] = item
	store := NewDynamoStore(client, testDynamoConfig())
	if err := store.UpdatePassword(context.Background(), "legacy", "new-hash", 1); err != nil {
		t.Fatal(err)
	}
	decoded, _, err = store.GetCustomerByID(context.Background(), "legacy")
	if err != nil || decoded.CredentialRevision != 1 {
		t.Fatalf("legacy update: %v", err)
	}
}

type consistentCustomerReadClient struct {
	*fakeCommerceClient
	t *testing.T
}

func (c consistentCustomerReadClient) GetItem(ctx context.Context, input *dynamodb.GetItemInput, options ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if stringAttribute(input.Key, "sk") == customerSK && !aws.ToBool(input.ConsistentRead) {
		c.t.Error("authorization customer read must be strongly consistent")
	}
	return c.fakeCommerceClient.GetItem(ctx, input, options...)
}
func TestCustomerAuthorizationReadIsConsistent(t *testing.T) {
	store := NewDynamoStore(consistentCustomerReadClient{newFakeCommerceClient(), t}, testDynamoConfig())
	customer := createTestCustomer(t, store, "consistent@example.test")
	if _, _, err := store.GetCustomerByID(context.Background(), customer.ID); err != nil {
		t.Fatal(err)
	}
}
