package main

import (
	"context"
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
	maxBatchWriteItems    = 25
)

type batchWriteClient interface {
	BatchWriteItem(ctx context.Context, params *dynamodb.BatchWriteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
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
	if err := batchWriteCatalogItems(ctx, client, *tableName, items); err != nil {
		log.Fatalf("seed catalog table: %v", err)
	}

	printSeedSummary("seeded", *tableName, *region, counts)
}

func batchWriteCatalogItems(ctx context.Context, client batchWriteClient, tableName string, items []map[string]types.AttributeValue) error {
	requests := make([]types.WriteRequest, 0, len(items))
	for _, item := range items {
		requests = append(requests, types.WriteRequest{
			PutRequest: &types.PutRequest{
				Item: item,
			},
		})
	}

	for start := 0; start < len(requests); start += maxBatchWriteItems {
		end := start + maxBatchWriteItems
		if end > len(requests) {
			end = len(requests)
		}
		if err := batchWriteChunk(ctx, client, tableName, requests[start:end]); err != nil {
			return fmt.Errorf("items %d-%d: %w", start+1, end, err)
		}
	}

	return nil
}

func batchWriteChunk(ctx context.Context, client batchWriteClient, tableName string, requests []types.WriteRequest) error {
	pending := requests
	for attempt := 1; len(pending) > 0; attempt++ {
		if attempt > maxBatchWriteAttempts {
			return fmt.Errorf("%d DynamoDB write requests remained unprocessed after %d attempts", len(pending), maxBatchWriteAttempts)
		}
		if attempt > 1 {
			if err := sleepBeforeRetry(ctx, attempt); err != nil {
				return err
			}
		}

		output, err := client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
			RequestItems: map[string][]types.WriteRequest{
				tableName: pending,
			},
		})
		if err != nil {
			return err
		}

		pending = output.UnprocessedItems[tableName]
	}

	return nil
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
		"%s %s in %s: %d categories, %d products, %d category-product rows, %d DynamoDB items\n",
		action,
		tableName,
		region,
		counts.Categories,
		counts.Products,
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
