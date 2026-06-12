package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	defaultRegion         = "us-east-1"
	defaultTableName      = "thailandgiftshop-catalog"
	maxBatchWriteAttempts = 8
)

type seedWriteClient interface {
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

func main() {
	log.SetFlags(0)

	tableName := flag.String("table", envOrDefault(catalog.EnvTableName, defaultTableName), "DynamoDB catalog table name")
	region := flag.String("region", envOrDefault("AWS_REGION", defaultRegion), "AWS region for the catalog table")
	dryRun := flag.Bool("dry-run", false, "Print the seed count without writing to DynamoDB")
	flag.Parse()

	items, err := catalog.DemoCatalogSeedItems()
	if err != nil {
		log.Fatalf("build demo catalog seed items: %v", err)
	}
	counts := catalog.DemoCatalogSeedCounts()

	if *dryRun {
		printSeedSummary("would seed", *tableName, *region, counts)
		return
	}

	ctx := context.Background()
	awsConfig, err := config.LoadDefaultConfig(ctx, config.WithRegion(*region))
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	client := dynamodb.NewFromConfig(awsConfig)
	if err := writeCatalogItems(ctx, client, *tableName, items); err != nil {
		log.Fatalf("seed catalog table: %v", err)
	}

	printSeedSummary("seeded", *tableName, *region, counts)
}

func writeCatalogItems(ctx context.Context, client seedWriteClient, tableName string, items []map[string]types.AttributeValue) error {
	for index, item := range items {
		if err := putCatalogItemIfMissing(ctx, client, tableName, item); err != nil {
			return fmt.Errorf("item %d: %w", index+1, err)
		}
	}

	return nil
}

func putCatalogItemIfMissing(ctx context.Context, client seedWriteClient, tableName string, item map[string]types.AttributeValue) error {
	var lastErr error
	for attempt := 1; attempt <= maxBatchWriteAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepBeforeRetry(ctx, attempt); err != nil {
				return err
			}
		}
		_, err := client.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:           &tableName,
			Item:                item,
			ConditionExpression: stringPtr("attribute_not_exists(#pk)"),
			ExpressionAttributeNames: map[string]string{
				"#pk": "pk",
			},
		})
		switch {
		case err == nil:
			return nil
		case isConditionalCheckFailed(err):
			return nil
		default:
			lastErr = err
		}
	}

	return fmt.Errorf("put item after %d attempts: %w", maxBatchWriteAttempts, lastErr)
}

func isConditionalCheckFailed(err error) bool {
	var conditional *types.ConditionalCheckFailedException
	return errors.As(err, &conditional)
}

func stringPtr(value string) *string {
	return &value
}

func sleepBeforeRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt-1) * 200 * time.Millisecond
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func printSeedSummary(action string, tableName string, region string, counts catalog.DemoSeedCounts) {
	fmt.Printf(
		"%s %s in %s: %d categories, %d products, %d product slug-lock rows, %d category-product rows, %d DynamoDB items\n",
		action,
		tableName,
		region,
		counts.Categories,
		counts.Products,
		counts.ProductSlugLockRows,
		counts.CategoryProductRows,
		counts.Items,
	)
}

func envOrDefault(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}

	return value
}
