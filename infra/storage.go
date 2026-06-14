package main

import (
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/jsii-runtime-go"
)

func addCatalog(stack awscdk.Stack) awsdynamodb.Table {
	catalogTable := awsdynamodb.NewTable(stack, jsii.String("CatalogTable"), &awsdynamodb.TableProps{
		BillingMode: awsdynamodb.BillingMode_PAY_PER_REQUEST,
		Encryption:  awsdynamodb.TableEncryption_AWS_MANAGED,
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogPartitionKeyName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		PointInTimeRecoverySpecification: &awsdynamodb.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: jsii.Bool(true),
		},
		RemovalPolicy: awscdk.RemovalPolicy_RETAIN,
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogSortKeyName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		TableName: jsii.String(catalogTableName),
	})

	catalogTable.AddGlobalSecondaryIndex(&awsdynamodb.GlobalSecondaryIndexProps{
		IndexName: jsii.String(catalog.DefaultSlugIndexName),
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogSlugIndexPKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		ProjectionType: awsdynamodb.ProjectionType_ALL,
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogSlugIndexSKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
	})
	catalogTable.AddGlobalSecondaryIndex(&awsdynamodb.GlobalSecondaryIndexProps{
		IndexName: jsii.String(catalog.DefaultPublicIndexName),
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogPublicIndexPKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		ProjectionType: awsdynamodb.ProjectionType_ALL,
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogPublicIndexSKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
	})
	catalogTable.AddGlobalSecondaryIndex(&awsdynamodb.GlobalSecondaryIndexProps{
		IndexName: jsii.String(catalog.DefaultRecentIndexName),
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogRecentIndexPKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		ProjectionType: awsdynamodb.ProjectionType_ALL,
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogRecentIndexSKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
	})
	catalogTable.AddGlobalSecondaryIndex(&awsdynamodb.GlobalSecondaryIndexProps{
		IndexName: jsii.String(catalog.DefaultEntityIndexName),
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogEntityIndexPKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		ProjectionType: awsdynamodb.ProjectionType_ALL,
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(catalogEntityIndexSKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
	})

	awscdk.NewCfnOutput(stack, jsii.String("CatalogTableName"), &awscdk.CfnOutputProps{
		Description: jsii.String("DynamoDB table name for products and categories"),
		Value:       catalogTable.TableName(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("CatalogTableArn"), &awscdk.CfnOutputProps{
		Description: jsii.String("DynamoDB table ARN for products and categories"),
		Value:       catalogTable.TableArn(),
	})

	return catalogTable
}

// addCommerce provisions the customer/order data home. Customer and order
// data outlives the stack, so the table is retained (deliberate contrast with
// the login-attempts table's DESTROY). TTL on expires_at lazily expires
// session, throttle, and Stripe-event rows.
func addCommerce(stack awscdk.Stack) awsdynamodb.Table {
	commerceTable := awsdynamodb.NewTable(stack, jsii.String("CommerceTable"), &awsdynamodb.TableProps{
		BillingMode: awsdynamodb.BillingMode_PAY_PER_REQUEST,
		Encryption:  awsdynamodb.TableEncryption_AWS_MANAGED,
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(commercePartitionKeyName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		PointInTimeRecoverySpecification: &awsdynamodb.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: jsii.Bool(true),
		},
		RemovalPolicy: awscdk.RemovalPolicy_RETAIN,
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(commerceSortKeyName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		TableName:           jsii.String(commerceTableName),
		TimeToLiveAttribute: jsii.String(commerceTTLAttributeName),
	})

	commerceTable.AddGlobalSecondaryIndex(&awsdynamodb.GlobalSecondaryIndexProps{
		IndexName: jsii.String(commerce.DefaultCustomerOrdersIndexName),
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(commerceCustomerOrdersIndexPKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		ProjectionType: awsdynamodb.ProjectionType_ALL,
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(commerceCustomerOrdersIndexSKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
	})
	commerceTable.AddGlobalSecondaryIndex(&awsdynamodb.GlobalSecondaryIndexProps{
		IndexName: jsii.String(commerce.DefaultOrdersIndexName),
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(commerceOrdersIndexPKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		ProjectionType: awsdynamodb.ProjectionType_ALL,
		SortKey: &awsdynamodb.Attribute{
			Name: jsii.String(commerceOrdersIndexSKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
	})

	awscdk.NewCfnOutput(stack, jsii.String("CommerceTableName"), &awscdk.CfnOutputProps{
		Description: jsii.String("DynamoDB table name for customers, carts, addresses, and orders"),
		Value:       commerceTable.TableName(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("CommerceTableArn"), &awscdk.CfnOutputProps{
		Description: jsii.String("DynamoDB table ARN for customers, carts, addresses, and orders"),
		Value:       commerceTable.TableArn(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("CommerceCustomerOrdersIndexName"), &awscdk.CfnOutputProps{
		Description: jsii.String("DynamoDB GSI name for a customer's order history"),
		Value:       jsii.String(commerce.DefaultCustomerOrdersIndexName),
	})
	awscdk.NewCfnOutput(stack, jsii.String("CommerceOrdersIndexName"), &awscdk.CfnOutputProps{
		Description: jsii.String("DynamoDB GSI name for the admin all-orders listing"),
		Value:       jsii.String(commerce.DefaultOrdersIndexName),
	})

	return commerceTable
}

func addProductImagesBucket(stack awscdk.Stack) awss3.Bucket {
	return awss3.NewBucket(stack, jsii.String("ProductImagesBucket"), &awss3.BucketProps{
		BlockPublicAccess: awss3.BlockPublicAccess_BLOCK_ALL(),
		Cors: &[]*awss3.CorsRule{
			{
				AllowedHeaders: &[]*string{
					jsii.String("*"),
				},
				AllowedMethods: &[]awss3.HttpMethods{
					awss3.HttpMethods_POST,
				},
				AllowedOrigins: &[]*string{
					jsii.String("https://" + siteDomainName),
					jsii.String("https://" + wwwDomainName),
				},
				MaxAge: jsii.Number(300),
			},
		},
		Encryption:      awss3.BucketEncryption_S3_MANAGED,
		EnforceSSL:      jsii.Bool(true),
		ObjectOwnership: awss3.ObjectOwnership_BUCKET_OWNER_ENFORCED,
		RemovalPolicy:   awscdk.RemovalPolicy_RETAIN,
	})
}

func addAdminLoginAttempts(stack awscdk.Stack) awsdynamodb.Table {
	return awsdynamodb.NewTable(stack, jsii.String("AdminLoginAttemptsTable"), &awsdynamodb.TableProps{
		BillingMode: awsdynamodb.BillingMode_PAY_PER_REQUEST,
		Encryption:  awsdynamodb.TableEncryption_AWS_MANAGED,
		PartitionKey: &awsdynamodb.Attribute{
			Name: jsii.String(adminLoginAttemptsPKName),
			Type: awsdynamodb.AttributeType_STRING,
		},
		PointInTimeRecoverySpecification: &awsdynamodb.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: jsii.Bool(true),
		},
		RemovalPolicy:       awscdk.RemovalPolicy_DESTROY,
		TimeToLiveAttribute: jsii.String(adminLoginAttemptsTTLName),
	})
}
