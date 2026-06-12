package main

import (
	"context"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type captureBatchWriteClient struct {
	inputs []*dynamodb.PutItemInput
}

func (c *captureBatchWriteClient) PutItem(ctx context.Context, input *dynamodb.PutItemInput, options ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	_ = ctx
	_ = options
	c.inputs = append(c.inputs, input)
	return &dynamodb.PutItemOutput{}, nil
}

func TestWriteCatalogItemsDoesNotOverwriteExistingRows(t *testing.T) {
	items, err := catalog.DemoCatalogSeedItems()
	if err != nil {
		t.Fatalf("DemoCatalogSeedItems returned error: %v", err)
	}
	client := &captureBatchWriteClient{}

	err = writeCatalogItems(context.Background(), client, "catalog-table", items)
	if err != nil {
		t.Fatalf("writeCatalogItems returned error: %v", err)
	}

	if len(client.inputs) == 0 {
		t.Fatal("PutItem was not called")
	}
	for _, input := range client.inputs {
		if input.ConditionExpression == nil || *input.ConditionExpression != "attribute_not_exists(#pk)" {
			t.Fatalf("PutItem condition = %v, want attribute_not_exists(#pk)", input.ConditionExpression)
		}
		if got := input.ExpressionAttributeNames["#pk"]; got != "pk" {
			t.Fatalf("PutItem #pk attribute name = %q, want pk", got)
		}
	}
}

func TestSeedCatalogItemsIncludeProductSlugLocks(t *testing.T) {
	items, err := catalog.DemoCatalogSeedItems()
	if err != nil {
		t.Fatalf("DemoCatalogSeedItems returned error: %v", err)
	}

	var slugLocks int
	for _, item := range items {
		entityType, ok := item["entity_type"].(*types.AttributeValueMemberS)
		if !ok || entityType.Value != "PRODUCT_SLUG_LOCK" {
			continue
		}
		slugLocks++
		if _, ok := item["gsi4pk"]; ok {
			t.Fatal("slug lock seed row must not carry entity-index attributes")
		}
	}
	if slugLocks != catalog.DemoCatalogSeedCounts().Products {
		t.Fatalf("slug lock rows = %d, want one per product %d", slugLocks, catalog.DemoCatalogSeedCounts().Products)
	}
}
