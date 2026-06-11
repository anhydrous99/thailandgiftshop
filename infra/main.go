package main

import (
	"os"
	"strings"

	adminauth "github.com/anhydrous99/thailandgiftshop/internal/admin"
	appenv "github.com/anhydrous99/thailandgiftshop/internal/appenv"
	cartsession "github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	appobservability "github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2integrations"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscertificatemanager"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfront"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfrontorigins"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudwatch"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudwatchactions"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53targets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3deployment"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssecretsmanager"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssns"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssnssubscriptions"
	"github.com/aws/aws-cdk-go/awscdk/v2/awswafv2"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

const (
	productionRegion = "us-east-1"
	siteDomainName   = "thailandgiftshop.com"
	wwwDomainName    = "www.thailandgiftshop.com"

	catalogTableName         = "thailandgiftshop-catalog"
	catalogPartitionKeyName  = "pk"
	catalogSortKeyName       = "sk"
	catalogSlugIndexPKName   = "gsi1pk"
	catalogSlugIndexSKName   = "gsi1sk"
	catalogPublicIndexPKName = "gsi2pk"
	catalogPublicIndexSKName = "gsi2sk"
	catalogRecentIndexPKName = "gsi3pk"
	catalogRecentIndexSKName = "gsi3sk"
	catalogEntityIndexPKName = "gsi4pk"
	catalogEntityIndexSKName = "gsi4sk"

	commerceTableName                 = "thailandgiftshop-commerce"
	commercePartitionKeyName          = "pk"
	commerceSortKeyName               = "sk"
	commerceCustomerOrdersIndexPKName = "gsi1pk"
	commerceCustomerOrdersIndexSKName = "gsi1sk"
	commerceOrdersIndexPKName         = "gsi2pk"
	commerceOrdersIndexSKName         = "gsi2sk"
	commerceTTLAttributeName          = "expires_at"

	staticAssetsKeyPrefix       = "static"
	productImagesKeyPrefix      = "images"
	adminCredentialsSecretName  = "thailandgiftshop/admin/credentials"
	stripeCredentialsSecretName = "thailandgiftshop/stripe/credentials"
	adminLambdaLogGroupName     = "/aws/lambda/thailandgiftshop-admin"
	adminLoginAttemptsPKName    = "client_key"
	adminLoginAttemptsTTLName   = "expires_at"

	ssrOriginRequestPolicyName = "thailandgiftshop-ssr-origin"
	ssrCachePolicyName         = "thailandgiftshop-ssr-cache"

	operationsDashboardName  = "ThailandGiftshop-Operations"
	operationsAlarmTopicName = "thailandgiftshop-operations-alarms"

	// Service dimension values the app emits in EMF; widgets and alarms must
	// query the same values the emitting packages record.
	metricServiceSsr      = "ssr"
	metricServiceCheckout = "checkout"
	metricServiceCatalog  = "catalog"

	ssrLambdaBuildCommand   = "mkdir -p cdk.out/ssr-lambda && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags lambda.norpc -ldflags \"-s -w\" -o cdk.out/ssr-lambda/bootstrap ../cmd/ssr"
	adminLambdaBuildCommand = "mkdir -p cdk.out/admin-lambda && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags lambda.norpc -ldflags \"-s -w\" -o cdk.out/admin-lambda/bootstrap ../cmd/admin"
)

type ThailandGiftshopStackProps struct {
	awscdk.StackProps
}

type ssrResources struct {
	httpAPI  awsapigatewayv2.HttpApi
	function awslambda.Function
	admin    adminResources
}

type adminResources struct {
	routes   []awsapigatewayv2.HttpRoute
	function awslambda.Function
}

type siteResources struct {
	distribution awscloudfront.Distribution
	adminWebACL  awswafv2.CfnWebACL
}

type observabilityResources struct {
	catalogTable            awsdynamodb.Table
	commerceTable           awsdynamodb.Table
	productImagesBucket     awss3.IBucket
	adminLoginAttemptsTable awsdynamodb.Table
	ssrFunction             awslambda.IFunction
	adminFunction           awslambda.IFunction
	httpAPI                 awsapigatewayv2.IHttpApi
	distribution            awscloudfront.Distribution
	adminWebACL             awswafv2.CfnWebACL
}

type dynamoMetricOperation struct {
	idSuffix  string
	dimension string
}

func NewThailandGiftshopStack(scope constructs.Construct, id string, props *ThailandGiftshopStackProps) awscdk.Stack {
	var stackProps awscdk.StackProps
	if props != nil {
		stackProps = props.StackProps
	}
	validateStackRegion(stackProps)

	stack := awscdk.NewStack(scope, &id, &stackProps)

	awscdk.Tags_Of(stack).Add(jsii.String("Project"), jsii.String("thailandgiftshop"), nil)
	awscdk.Tags_Of(stack).Add(jsii.String("ManagedBy"), jsii.String("aws-cdk"), nil)

	catalogTable := addCatalog(stack)
	commerceTable := addCommerce(stack)
	productImagesBucket := addProductImagesBucket(stack)
	adminLoginAttemptsTable := addAdminLoginAttempts(stack)
	adminOriginHeaderSecret := addAdminOriginHeaderSecret(stack)
	ssr := addSSR(stack, catalogTable, commerceTable, productImagesBucket, adminLoginAttemptsTable, adminOriginHeaderSecret)
	site := addSite(stack, ssr.httpAPI, productImagesBucket, adminOriginHeaderSecret)
	addObservability(stack, observabilityResources{
		catalogTable:            catalogTable,
		commerceTable:           commerceTable,
		productImagesBucket:     productImagesBucket,
		adminLoginAttemptsTable: adminLoginAttemptsTable,
		ssrFunction:             ssr.function,
		adminFunction:           ssr.admin.function,
		httpAPI:                 ssr.httpAPI,
		distribution:            site.distribution,
		adminWebACL:             site.adminWebACL,
	})

	return stack
}

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

func addSSR(stack awscdk.Stack, catalogTable awsdynamodb.ITable, commerceTable awsdynamodb.ITable, productImagesBucket awss3.IBucket, adminLoginAttemptsTable awsdynamodb.ITable, adminOriginHeaderSecret awssecretsmanager.ISecret) ssrResources {
	lambdaLogGroup := awslogs.NewLogGroup(stack, jsii.String("SsrLambdaLogGroup"), &awslogs.LogGroupProps{
		LogGroupName: jsii.String("/aws/lambda/thailandgiftshop-ssr"),
		Retention:    awslogs.RetentionDays_THREE_MONTHS,
	})
	cartCookieSecret := addCartCookieSecret(stack)
	customerSessionSecret := addCustomerSessionSecret(stack)
	stripeSecret := stripeCredentialsSecret(stack)

	ssrFunction := awslambda.NewFunction(stack, jsii.String("SsrLambda"), &awslambda.FunctionProps{
		Architecture: awslambda.Architecture_ARM_64(),
		Code: awslambda.Code_FromCustomCommand(jsii.String("cdk.out/ssr-lambda"), &[]*string{
			jsii.String("sh"),
			jsii.String("-c"),
			jsii.String(ssrLambdaBuildCommand),
		}, &awslambda.CustomCommandOptions{
			DeployTime:  jsii.Bool(true),
			DisplayName: jsii.String("ssr-lambda"),
		}),
		Description: jsii.String("Server-side HTML renderer for thailandgiftshop.com"),
		Environment: &map[string]*string{
			catalog.EnvTableName:                    catalogTable.TableName(),
			catalog.EnvSlugIndexName:                jsii.String(catalog.DefaultSlugIndexName),
			catalog.EnvPublicIndexName:              jsii.String(catalog.DefaultPublicIndexName),
			catalog.EnvRecentIndexName:              jsii.String(catalog.DefaultRecentIndexName),
			catalog.EnvEntityIndexName:              jsii.String(catalog.DefaultEntityIndexName),
			catalog.EnvProductImagePlaceholderURL:   jsii.String(catalog.DefaultProductImagePlaceholderURL),
			appenv.EnvAppEnvironment:                jsii.String(appenv.EnvironmentProduction),
			cartsession.EnvCookieSecret:             cartCookieSecretReference(cartCookieSecret),
			adminauth.EnvAdminOriginHeaderSecret:    adminOriginHeaderSecretReference(adminOriginHeaderSecret),
			commerce.EnvTableName:                   commerceTable.TableName(),
			commerce.EnvCustomerOrdersIndexName:     jsii.String(commerce.DefaultCustomerOrdersIndexName),
			commerce.EnvOrdersIndexName:             jsii.String(commerce.DefaultOrdersIndexName),
			commerce.EnvSessionSecret:               customerSessionSecretReference(customerSessionSecret),
			payments.EnvStripeCredentialsSecretName: jsii.String(stripeCredentialsSecretName),
			payments.EnvPublicBaseURL:               jsii.String(payments.DefaultPublicBaseURL),
		},
		FunctionName: jsii.String("thailandgiftshop-ssr"),
		Handler:      jsii.String("bootstrap"),
		LogGroup:     lambdaLogGroup,
		MemorySize:   jsii.Number(512),
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(10)),
		Tracing:      awslambda.Tracing_ACTIVE,
	})
	// Checkout reserves and releases stock through the catalog store's
	// versioned UpdateProduct transaction, so the public SSR Lambda needs
	// write access to the catalog table in addition to its reads.
	catalogTable.GrantReadWriteData(ssrFunction)
	ssrFunction.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("dynamodb:TransactWriteItems"),
		},
		Resources: &[]*string{
			catalogTable.TableArn(),
		},
	}))
	commerceTable.GrantReadWriteData(ssrFunction)
	stripeSecret.GrantRead(ssrFunction, nil)
	ssrFunction.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("dynamodb:TransactWriteItems"),
		},
		Resources: &[]*string{
			commerceTable.TableArn(),
		},
	}))

	httpAPI := awsapigatewayv2.NewHttpApi(stack, jsii.String("SsrHttpApi"), &awsapigatewayv2.HttpApiProps{
		ApiName:            jsii.String("thailandgiftshop-ssr"),
		CreateDefaultStage: jsii.Bool(false),
		Description:        jsii.String("HTTP API for thailandgiftshop.com HTML rendering"),
	})

	defaultRoute := awsapigatewayv2.NewHttpRoute(stack, jsii.String("SsrDefaultRoute"), &awsapigatewayv2.HttpRouteProps{
		HttpApi:  httpAPI,
		RouteKey: awsapigatewayv2.HttpRouteKey_DEFAULT(),
		Integration: awsapigatewayv2integrations.NewHttpLambdaIntegration(jsii.String("SsrLambdaIntegration"), ssrFunction, &awsapigatewayv2integrations.HttpLambdaIntegrationProps{
			PayloadFormatVersion: awsapigatewayv2.PayloadFormatVersion_VERSION_2_0(),
			Timeout:              awscdk.Duration_Seconds(jsii.Number(10)),
		}),
	})
	admin := addAdmin(stack, catalogTable, commerceTable, productImagesBucket, adminLoginAttemptsTable, adminOriginHeaderSecret, stripeSecret, httpAPI)

	accessLogGroup := awslogs.NewLogGroup(stack, jsii.String("SsrHttpApiAccessLogGroup"), &awslogs.LogGroupProps{
		LogGroupName: jsii.String("/aws/apigateway/thailandgiftshop-ssr"),
		Retention:    awslogs.RetentionDays_THREE_MONTHS,
	})

	stage := awsapigatewayv2.NewCfnStage(stack, jsii.String("SsrHttpApiDefaultStage"), &awsapigatewayv2.CfnStageProps{
		AccessLogSettings: &awsapigatewayv2.CfnStage_AccessLogSettingsProperty{
			DestinationArn: accessLogGroup.LogGroupArn(),
			Format:         jsii.String(`{"requestId":"$context.requestId","ip":"$context.identity.sourceIp","requestTime":"$context.requestTime","httpMethod":"$context.httpMethod","routeKey":"$context.routeKey","status":"$context.status","protocol":"$context.protocol","responseLength":"$context.responseLength","extendedRequestId":"$context.extendedRequestId","integrationError":"$context.integrationErrorMessage"}`),
		},
		ApiId:      httpAPI.HttpApiId(),
		AutoDeploy: jsii.Bool(true),
		DefaultRouteSettings: &awsapigatewayv2.CfnStage_RouteSettingsProperty{
			DetailedMetricsEnabled: jsii.Bool(true),
		},
		Description: jsii.String("Default stage for SSR HTTP API"),
		StageName:   jsii.String("$default"),
	})
	stage.Node().AddDependency(defaultRoute)
	for _, route := range admin.routes {
		stage.Node().AddDependency(route)
	}

	awscdk.NewCfnOutput(stack, jsii.String("SsrHttpApiUrl"), &awscdk.CfnOutputProps{
		Description: jsii.String("Base URL for the SSR HTTP API"),
		Value:       httpAPI.ApiEndpoint(),
	})

	return ssrResources{httpAPI: httpAPI, function: ssrFunction, admin: admin}
}

func addAdmin(stack awscdk.Stack, catalogTable awsdynamodb.ITable, commerceTable awsdynamodb.ITable, productImagesBucket awss3.IBucket, adminLoginAttemptsTable awsdynamodb.ITable, adminOriginHeaderSecret awssecretsmanager.ISecret, stripeSecret awssecretsmanager.ISecret, httpAPI awsapigatewayv2.HttpApi) adminResources {
	lambdaLogGroup := awslogs.LogGroup_FromLogGroupName(stack, jsii.String("AdminLambdaLogGroup"), jsii.String(adminLambdaLogGroupName))
	awslogs.NewLogRetention(stack, jsii.String("AdminLambdaLogRetention"), &awslogs.LogRetentionProps{
		LogGroupName:  jsii.String(adminLambdaLogGroupName),
		RemovalPolicy: awscdk.RemovalPolicy_RETAIN,
		Retention:     awslogs.RetentionDays_THREE_MONTHS,
	})

	adminFunction := awslambda.NewFunction(stack, jsii.String("AdminLambda"), &awslambda.FunctionProps{
		Architecture: awslambda.Architecture_ARM_64(),
		Code: awslambda.Code_FromCustomCommand(jsii.String("cdk.out/admin-lambda"), &[]*string{
			jsii.String("sh"),
			jsii.String("-c"),
			jsii.String(adminLambdaBuildCommand),
		}, &awslambda.CustomCommandOptions{
			DeployTime:  jsii.Bool(true),
			DisplayName: jsii.String("admin-lambda"),
		}),
		Description: jsii.String("Admin HTML handler for thailandgiftshop.com"),
		Environment: &map[string]*string{
			catalog.EnvTableName:                     catalogTable.TableName(),
			catalog.EnvSlugIndexName:                 jsii.String(catalog.DefaultSlugIndexName),
			catalog.EnvPublicIndexName:               jsii.String(catalog.DefaultPublicIndexName),
			catalog.EnvRecentIndexName:               jsii.String(catalog.DefaultRecentIndexName),
			catalog.EnvEntityIndexName:               jsii.String(catalog.DefaultEntityIndexName),
			adminauth.EnvProductImagesBucketName:     productImagesBucket.BucketName(),
			adminauth.EnvProductImagesKeyPrefix:      jsii.String(productImagesKeyPrefix),
			catalog.EnvProductImagePlaceholderURL:    jsii.String(catalog.DefaultProductImagePlaceholderURL),
			appenv.EnvAppEnvironment:                 jsii.String(appenv.EnvironmentProduction),
			adminauth.EnvAdminCredentialsSecretName:  jsii.String(adminCredentialsSecretName),
			adminauth.EnvAdminLoginAttemptsTableName: adminLoginAttemptsTable.TableName(),
			adminauth.EnvAdminOriginHeaderSecret:     adminOriginHeaderSecretReference(adminOriginHeaderSecret),
			commerce.EnvTableName:                    commerceTable.TableName(),
			commerce.EnvCustomerOrdersIndexName:      jsii.String(commerce.DefaultCustomerOrdersIndexName),
			commerce.EnvOrdersIndexName:              jsii.String(commerce.DefaultOrdersIndexName),
			// Admin cancels of pending orders verify (and expire) the order's
			// Stripe checkout session before releasing stock; without the
			// credentials the handler skips that session-expiry guard.
			payments.EnvStripeCredentialsSecretName: jsii.String(stripeCredentialsSecretName),
		},
		FunctionName: jsii.String("thailandgiftshop-admin"),
		Handler:      jsii.String("bootstrap"),
		LogGroup:     lambdaLogGroup,
		MemorySize:   jsii.Number(512),
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(10)),
		Tracing:      awslambda.Tracing_ACTIVE,
	})
	catalogTable.GrantReadWriteData(adminFunction)
	adminFunction.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("dynamodb:TransactWriteItems"),
		},
		Resources: &[]*string{
			catalogTable.TableArn(),
		},
	}))
	commerceTable.GrantReadWriteData(adminFunction)
	stripeSecret.GrantRead(adminFunction, nil)
	// The admin Lambda reads its credentials JSON from Secrets Manager at
	// runtime (not a deploy-time dynamic reference), so a rotation takes effect
	// without redeploying.
	adminCredentialsSecret(stack).GrantRead(adminFunction, nil)
	adminFunction.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("dynamodb:TransactWriteItems"),
		},
		Resources: &[]*string{
			commerceTable.TableArn(),
		},
	}))
	productImagesBucket.GrantRead(adminFunction, jsii.String(productImagesKeyPrefix+"/*"))
	productImagesBucket.GrantPut(adminFunction, jsii.String(productImagesKeyPrefix+"/*"))
	adminFunction.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("dynamodb:GetItem"),
			jsii.String("dynamodb:UpdateItem"),
			jsii.String("dynamodb:DeleteItem"),
		},
		Resources: &[]*string{
			adminLoginAttemptsTable.TableArn(),
		},
	}))

	adminIntegration := awsapigatewayv2integrations.NewHttpLambdaIntegration(jsii.String("AdminLambdaIntegration"), adminFunction, &awsapigatewayv2integrations.HttpLambdaIntegrationProps{
		PayloadFormatVersion: awsapigatewayv2.PayloadFormatVersion_VERSION_2_0(),
		Timeout:              awscdk.Duration_Seconds(jsii.Number(10)),
	})
	exactRoute := awsapigatewayv2.NewHttpRoute(stack, jsii.String("AdminRoute"), &awsapigatewayv2.HttpRouteProps{
		HttpApi:     httpAPI,
		RouteKey:    awsapigatewayv2.HttpRouteKey_With(jsii.String("/admin"), awsapigatewayv2.HttpMethod_ANY),
		Integration: adminIntegration,
	})
	proxyRoute := awsapigatewayv2.NewHttpRoute(stack, jsii.String("AdminProxyRoute"), &awsapigatewayv2.HttpRouteProps{
		HttpApi:     httpAPI,
		RouteKey:    awsapigatewayv2.HttpRouteKey_With(jsii.String("/admin/{proxy+}"), awsapigatewayv2.HttpMethod_ANY),
		Integration: adminIntegration,
	})

	return adminResources{routes: []awsapigatewayv2.HttpRoute{exactRoute, proxyRoute}, function: adminFunction}
}

func adminCredentialsSecret(stack awscdk.Stack) awssecretsmanager.ISecret {
	return awssecretsmanager.Secret_FromSecretNameV2(stack, jsii.String("AdminCredentialsSecret"), jsii.String(adminCredentialsSecretName))
}

func addAdminCloudFrontWebACL(stack awscdk.Stack) awswafv2.CfnWebACL {
	return adminRateLimitWebACL(stack, "AdminCloudFrontWebACL", "thailandgiftshop-admin-cloudfront", "CLOUDFRONT", "ThailandGiftshopAdminCloudFront")
}

func adminRateLimitWebACL(stack awscdk.Stack, id string, name string, scope string, metricName string) awswafv2.CfnWebACL {
	return awswafv2.NewCfnWebACL(stack, jsii.String(id), &awswafv2.CfnWebACLProps{
		Name:  jsii.String(name),
		Scope: jsii.String(scope),
		DefaultAction: &awswafv2.CfnWebACL_DefaultActionProperty{
			Allow: &awswafv2.CfnWebACL_AllowActionProperty{},
		},
		VisibilityConfig: wafVisibility(metricName),
		Rules: []any{
			adminRateLimitRule("AdminLoginPostRateLimit", 0, 100, loginPostStatement(), metricName+"LoginPost"),
			adminRateLimitRule("AdminPathRateLimit", 1, 500, adminPathStatement(), metricName+"Path"),
			adminRateLimitRule("CustomerAuthPostRateLimit", 2, 100, customerAuthPostStatement(), metricName+"CustomerAuthPost"),
			adminRateLimitRule("CheckoutPlaceOrderRateLimit", 3, 100, checkoutPlaceOrderPostStatement(), metricName+"CheckoutPlaceOrderPost"),
		},
	})
}

func adminRateLimitRule(name string, priority int, limit int, statement any, metricName string) *awswafv2.CfnWebACL_RuleProperty {
	return &awswafv2.CfnWebACL_RuleProperty{
		Name:     jsii.String(name),
		Priority: jsii.Number(priority),
		Action: &awswafv2.CfnWebACL_RuleActionProperty{
			Block: &awswafv2.CfnWebACL_BlockActionProperty{
				CustomResponse: &awswafv2.CfnWebACL_CustomResponseProperty{
					ResponseCode: jsii.Number(429),
				},
			},
		},
		Statement: &awswafv2.CfnWebACL_StatementProperty{
			RateBasedStatement: &awswafv2.CfnWebACL_RateBasedStatementProperty{
				AggregateKeyType:    jsii.String("IP"),
				Limit:               jsii.Number(limit),
				EvaluationWindowSec: jsii.Number(300),
				ScopeDownStatement:  statement,
			},
		},
		VisibilityConfig: wafVisibility(metricName),
	}
}

func loginPostStatement() *awswafv2.CfnWebACL_StatementProperty {
	return &awswafv2.CfnWebACL_StatementProperty{
		AndStatement: &awswafv2.CfnWebACL_AndStatementProperty{
			Statements: []any{
				urlDecodedPathStatement("/admin/login", "EXACTLY"),
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{Method: map[string]any{}}, "POST", "EXACTLY"),
			},
		},
	}
}

// customerAuthPostStatement scopes the customer sign-in/sign-up rate rule to
// POSTs against the two pre-session auth endpoints. The Stripe webhook path is
// deliberately not rate-limited here: Stripe burst-retries, and the origin
// secret plus webhook signature already gate it.
func customerAuthPostStatement() *awswafv2.CfnWebACL_StatementProperty {
	return &awswafv2.CfnWebACL_StatementProperty{
		AndStatement: &awswafv2.CfnWebACL_AndStatementProperty{
			Statements: []any{
				&awswafv2.CfnWebACL_StatementProperty{
					OrStatement: &awswafv2.CfnWebACL_OrStatementProperty{
						Statements: []any{
							urlDecodedPathStatement("/account/sign-in", "EXACTLY"),
							urlDecodedPathStatement("/account/sign-up", "EXACTLY"),
						},
					},
				},
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{Method: map[string]any{}}, "POST", "EXACTLY"),
			},
		},
	}
}

// checkoutPlaceOrderPostStatement rate-limits the one unauthenticated POST
// that reserves stock. Guest checkout removes the account gate, so the edge
// throttle takes its place; 100 place-order POSTs per 5 minutes per IP is far
// beyond any human checkout cadence.
func checkoutPlaceOrderPostStatement() *awswafv2.CfnWebACL_StatementProperty {
	return &awswafv2.CfnWebACL_StatementProperty{
		AndStatement: &awswafv2.CfnWebACL_AndStatementProperty{
			Statements: []any{
				urlDecodedPathStatement("/checkout/place-order", "EXACTLY"),
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{Method: map[string]any{}}, "POST", "EXACTLY"),
			},
		},
	}
}

func adminPathStatement() *awswafv2.CfnWebACL_StatementProperty {
	return &awswafv2.CfnWebACL_StatementProperty{
		OrStatement: &awswafv2.CfnWebACL_OrStatementProperty{
			Statements: []any{
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{UriPath: map[string]any{}}, "/admin", "EXACTLY"),
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{UriPath: map[string]any{}}, "/admin/", "STARTS_WITH"),
			},
		},
	}
}

func byteMatchStatement(fieldToMatch any, search string, positionalConstraint string) *awswafv2.CfnWebACL_StatementProperty {
	return transformedByteMatchStatement(fieldToMatch, search, positionalConstraint, []string{"NONE"})
}

// urlDecodedPathStatement matches a URI path with URL_DECODE applied before
// the literal comparison, so percent-encoded spellings of a rate-limited path
// (for example /account/sign%2Din) still count toward the edge rate rules.
// WAF applies TextTransformations in priority order and inspects the final
// value, so decoding leaves canonical paths byte-identical to a NONE match.
func urlDecodedPathStatement(search string, positionalConstraint string) *awswafv2.CfnWebACL_StatementProperty {
	return transformedByteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{UriPath: map[string]any{}}, search, positionalConstraint, []string{"URL_DECODE", "NONE"})
}

func transformedByteMatchStatement(fieldToMatch any, search string, positionalConstraint string, transformations []string) *awswafv2.CfnWebACL_StatementProperty {
	textTransformations := make([]any, 0, len(transformations))
	for priority, transformation := range transformations {
		textTransformations = append(textTransformations, &awswafv2.CfnWebACL_TextTransformationProperty{
			Priority: jsii.Number(priority),
			Type:     jsii.String(transformation),
		})
	}

	return &awswafv2.CfnWebACL_StatementProperty{
		ByteMatchStatement: &awswafv2.CfnWebACL_ByteMatchStatementProperty{
			FieldToMatch:         fieldToMatch,
			SearchString:         jsii.String(search),
			PositionalConstraint: jsii.String(positionalConstraint),
			TextTransformations:  textTransformations,
		},
	}
}

func wafVisibility(metricName string) *awswafv2.CfnWebACL_VisibilityConfigProperty {
	return &awswafv2.CfnWebACL_VisibilityConfigProperty{
		CloudWatchMetricsEnabled: jsii.Bool(true),
		MetricName:               jsii.String(metricName),
		SampledRequestsEnabled:   jsii.Bool(true),
	}
}

func addAdminOriginHeaderSecret(stack awscdk.Stack) awssecretsmanager.Secret {
	return awssecretsmanager.NewSecret(stack, jsii.String("AdminOriginHeaderSecret"), &awssecretsmanager.SecretProps{
		Description: jsii.String("Shared origin header secret for thailandgiftshop.com admin requests through CloudFront"),
		GenerateSecretString: &awssecretsmanager.SecretStringGenerator{
			ExcludePunctuation: jsii.Bool(true),
			PasswordLength:     jsii.Number(64),
		},
	})
}

func ssrOriginRequestPolicy(stack awscdk.Stack) awscloudfront.OriginRequestPolicy {
	return awscloudfront.NewOriginRequestPolicy(stack, jsii.String("SsrOriginRequestPolicy"), &awscloudfront.OriginRequestPolicyProps{
		OriginRequestPolicyName: jsii.String(ssrOriginRequestPolicyName),
		Comment:                 jsii.String("Headers, cookies, and query strings for thailandgiftshop.com SSR origin"),
		CookieBehavior:          awscloudfront.OriginRequestCookieBehavior_All(),
		QueryStringBehavior:     awscloudfront.OriginRequestQueryStringBehavior_All(),
		HeaderBehavior: awscloudfront.OriginRequestHeaderBehavior_AllowList(
			jsii.String("CloudFront-Viewer-Address"),
			jsii.String("CloudFront-Forwarded-Proto"),
			jsii.String("Content-Type"),
			jsii.String("HX-Request"),
			jsii.String("X-CSRF-Token"),
			jsii.String("Stripe-Signature"),
		),
	})
}

// ssrCachePolicy caches only responses that opt in with an explicit cacheable
// Cache-Control header (DefaultTtl is zero), keyed on the cart cookie so a
// signed-in cart's "Cart (N)" header never serves to another visitor. Cart,
// checkout, account, and admin responses send no-store and stay uncached; the
// customer session cookie joins the cache key as defense-in-depth so an authed
// response could never serve to another visitor even if a page mis-opted in.
func ssrCachePolicy(stack awscdk.Stack) awscloudfront.CachePolicy {
	return awscloudfront.NewCachePolicy(stack, jsii.String("SsrCachePolicy"), &awscloudfront.CachePolicyProps{
		CachePolicyName:            jsii.String(ssrCachePolicyName),
		Comment:                    jsii.String("Cache opt-in SSR responses for thailandgiftshop.com keyed on the cart cookie"),
		CookieBehavior:             awscloudfront.CacheCookieBehavior_AllowList(jsii.String(cartsession.CookieName), jsii.String(commerce.SessionCookieName)),
		QueryStringBehavior:        awscloudfront.CacheQueryStringBehavior_All(),
		HeaderBehavior:             awscloudfront.CacheHeaderBehavior_None(),
		MinTtl:                     awscdk.Duration_Seconds(jsii.Number(0)),
		DefaultTtl:                 awscdk.Duration_Seconds(jsii.Number(0)),
		MaxTtl:                     awscdk.Duration_Days(jsii.Number(1)),
		EnableAcceptEncodingGzip:   jsii.Bool(true),
		EnableAcceptEncodingBrotli: jsii.Bool(true),
	})
}

func adminOriginHeaderSecretReference(secret awssecretsmanager.ISecret) *string {
	return awscdk.NewCfnDynamicReference(
		awscdk.CfnDynamicReferenceService_SECRETS_MANAGER,
		secret.CfnDynamicReferenceKey(nil),
	).ToString()
}

func addCartCookieSecret(stack awscdk.Stack) awssecretsmanager.Secret {
	return awssecretsmanager.NewSecret(stack, jsii.String("CartCookieSecret"), &awssecretsmanager.SecretProps{
		Description: jsii.String("Signing secret for thailandgiftshop.com cart cookies"),
		GenerateSecretString: &awssecretsmanager.SecretStringGenerator{
			ExcludePunctuation: jsii.Bool(true),
			PasswordLength:     jsii.Number(64),
		},
	})
}

func cartCookieSecretReference(secret awssecretsmanager.ISecret) *string {
	return awscdk.NewCfnDynamicReference(
		awscdk.CfnDynamicReferenceService_SECRETS_MANAGER,
		secret.CfnDynamicReferenceKey(nil),
	).ToString()
}

func addCustomerSessionSecret(stack awscdk.Stack) awssecretsmanager.Secret {
	return awssecretsmanager.NewSecret(stack, jsii.String("CustomerSessionSecret"), &awssecretsmanager.SecretProps{
		Description: jsii.String("Signing secret for thailandgiftshop.com customer session cookies"),
		GenerateSecretString: &awssecretsmanager.SecretStringGenerator{
			ExcludePunctuation: jsii.Bool(true),
			PasswordLength:     jsii.Number(64),
		},
	})
}

func customerSessionSecretReference(secret awssecretsmanager.ISecret) *string {
	return awscdk.NewCfnDynamicReference(
		awscdk.CfnDynamicReferenceService_SECRETS_MANAGER,
		secret.CfnDynamicReferenceKey(nil),
	).ToString()
}

// stripeCredentialsSecret references the manually pre-created Stripe secret
// (JSON {"secret_key","webhook_signing_secret"}) in us-east-1 — the
// adminCredentialsSecret precedent. It must exist before the first deploy.
func stripeCredentialsSecret(stack awscdk.Stack) awssecretsmanager.ISecret {
	return awssecretsmanager.Secret_FromSecretNameV2(stack, jsii.String("StripeCredentialsSecret"), jsii.String(stripeCredentialsSecretName))
}

func addCanonicalHostRedirectFunction(stack awscdk.Stack) awscloudfront.Function {
	return awscloudfront.NewFunction(stack, jsii.String("CanonicalHostRedirectFunction"), &awscloudfront.FunctionProps{
		Code: awscloudfront.FunctionCode_FromInline(jsii.String(`function handler(event) {
    var request = event.request;
    var host = request.headers.host.value.toLowerCase();

    if (host !== "www.thailandgiftshop.com") {
        return request;
    }

    var location = "https://thailandgiftshop.com" + request.uri;
    var querystring = request.querystring;
    var queryParts = [];

    for (var name in querystring) {
        if (!Object.prototype.hasOwnProperty.call(querystring, name)) {
            continue;
        }

        var parameter = querystring[name];
        if (parameter.multiValue) {
            for (var index = 0; index < parameter.multiValue.length; index++) {
                queryParts.push(name + "=" + parameter.multiValue[index].value);
            }
            continue;
        }

        queryParts.push(name + "=" + parameter.value);
    }

    if (queryParts.length > 0) {
        location += "?" + queryParts.join("&");
    }

    return {
        statusCode: 308,
        statusDescription: "Permanent Redirect",
        headers: {
            location: {
                value: location
            }
        }
    };
}`)),
		Comment:      jsii.String("Redirect www.thailandgiftshop.com requests to the apex host"),
		FunctionName: jsii.String("thailandgiftshop-www-to-apex"),
		Runtime:      awscloudfront.FunctionRuntime_JS_2_0(),
	})
}

func canonicalHostRedirectAssociations(function awscloudfront.Function) *[]*awscloudfront.FunctionAssociation {
	return &[]*awscloudfront.FunctionAssociation{
		{
			EventType: awscloudfront.FunctionEventType_VIEWER_REQUEST,
			Function:  function,
		},
	}
}

func addSite(stack awscdk.Stack, httpAPI awsapigatewayv2.HttpApi, productImagesBucket awss3.IBucket, adminOriginHeaderSecret awssecretsmanager.ISecret) siteResources {
	hostedZone := siteHostedZone(stack)
	certificate := awscertificatemanager.NewCertificate(stack, jsii.String("SiteCertificate"), &awscertificatemanager.CertificateProps{
		DomainName: jsii.String(siteDomainName),
		SubjectAlternativeNames: &[]*string{
			jsii.String(wwwDomainName),
		},
		Validation: awscertificatemanager.CertificateValidation_FromDns(hostedZone),
	})
	securityHeadersPolicy := siteSecurityHeaders(stack)
	adminWebACL := addAdminCloudFrontWebACL(stack)
	originRequestPolicy := ssrOriginRequestPolicy(stack)
	cachePolicy := ssrCachePolicy(stack)
	canonicalHostRedirectFunction := addCanonicalHostRedirectFunction(stack)
	canonicalHostRedirectFunctionAssociations := canonicalHostRedirectAssociations(canonicalHostRedirectFunction)

	staticBucket := awss3.NewBucket(stack, jsii.String("StaticAssetsBucket"), &awss3.BucketProps{
		BlockPublicAccess: awss3.BlockPublicAccess_BLOCK_ALL(),
		Encryption:        awss3.BucketEncryption_S3_MANAGED,
		EnforceSSL:        jsii.Bool(true),
		ObjectOwnership:   awss3.ObjectOwnership_BUCKET_OWNER_ENFORCED,
		RemovalPolicy:     awscdk.RemovalPolicy_RETAIN,
	})
	staticAssetsDeploymentLogGroup := awslogs.NewLogGroup(stack, jsii.String("StaticAssetsDeploymentLogGroup"), &awslogs.LogGroupProps{
		LogGroupName: jsii.String("/aws/lambda/thailandgiftshop-static-assets-deployment"),
		Retention:    awslogs.RetentionDays_THREE_MONTHS,
	})

	distribution := awscloudfront.NewDistribution(stack, jsii.String("SiteDistribution"), &awscloudfront.DistributionProps{
		Certificate: certificate,
		Comment:     jsii.String("CloudFront distribution for thailandgiftshop.com SSR and static assets"),
		DomainNames: &[]*string{
			jsii.String(siteDomainName),
			jsii.String(wwwDomainName),
		},
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_ALL(),
			CachePolicy:           cachePolicy,
			Compress:              jsii.Bool(true),
			Origin:                ssrOrigin(httpAPI, adminOriginHeaderSecret),
			OriginRequestPolicy:   originRequestPolicy,
			ResponseHeadersPolicy: securityHeadersPolicy,
			ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
			FunctionAssociations:  canonicalHostRedirectFunctionAssociations,
		},
		AdditionalBehaviors: &map[string]*awscloudfront.BehaviorOptions{
			"static/*": {
				AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_GET_HEAD(),
				CachePolicy:           awscloudfront.CachePolicy_CACHING_OPTIMIZED(),
				Compress:              jsii.Bool(true),
				Origin:                awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(staticBucket, nil),
				ResponseHeadersPolicy: securityHeadersPolicy,
				ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
				FunctionAssociations:  canonicalHostRedirectFunctionAssociations,
			},
			"images/*": {
				AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_GET_HEAD(),
				CachePolicy:           awscloudfront.CachePolicy_CACHING_OPTIMIZED(),
				Compress:              jsii.Bool(true),
				Origin:                awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(productImagesBucket, nil),
				ResponseHeadersPolicy: securityHeadersPolicy,
				ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
				FunctionAssociations:  canonicalHostRedirectFunctionAssociations,
			},
		},
		WebAclId: adminWebACL.AttrArn(),
	})

	addSiteAliasRecords(stack, hostedZone, distribution)

	awss3deployment.NewBucketDeployment(stack, jsii.String("StaticAssetsDeployment"), &awss3deployment.BucketDeploymentProps{
		CacheControl: &[]awss3deployment.CacheControl{
			awss3deployment.CacheControl_MaxAge(awscdk.Duration_Hours(jsii.Number(1))),
		},
		DestinationBucket:    staticBucket,
		DestinationKeyPrefix: jsii.String(staticAssetsKeyPrefix),
		Distribution:         distribution,
		DistributionPaths: &[]*string{
			jsii.String("/static/*"),
		},
		LogGroup: staticAssetsDeploymentLogGroup,
		Prune:    jsii.Bool(true),
		Sources: &[]awss3deployment.ISource{
			awss3deployment.Source_Asset(jsii.String("../web/static"), nil),
		},
	})
	awss3deployment.NewBucketDeployment(stack, jsii.String("ProductImagesDeployment"), &awss3deployment.BucketDeploymentProps{
		CacheControl: &[]awss3deployment.CacheControl{
			awss3deployment.CacheControl_MaxAge(awscdk.Duration_Hours(jsii.Number(1))),
		},
		DestinationBucket:    productImagesBucket,
		DestinationKeyPrefix: jsii.String(productImagesKeyPrefix),
		Distribution:         distribution,
		DistributionPaths: &[]*string{
			jsii.String("/images/*"),
		},
		LogGroup: staticAssetsDeploymentLogGroup,
		Prune:    jsii.Bool(false),
		Sources: &[]awss3deployment.ISource{
			awss3deployment.Source_Asset(jsii.String("../web/product-images"), nil),
		},
	})

	awscdk.NewCfnOutput(stack, jsii.String("SiteDistributionDomainName"), &awscdk.CfnOutputProps{
		Description: jsii.String("CloudFront domain name for thailandgiftshop.com"),
		Value:       distribution.DistributionDomainName(),
	})

	awscdk.NewCfnOutput(stack, jsii.String("SiteUrl"), &awscdk.CfnOutputProps{
		Description: jsii.String("CloudFront URL for thailandgiftshop.com"),
		Value:       jsii.String("https://" + siteDomainName),
	})
	awscdk.NewCfnOutput(stack, jsii.String("ProductImagesBucketName"), &awscdk.CfnOutputProps{
		Description: jsii.String("S3 bucket name for product image objects"),
		Value:       productImagesBucket.BucketName(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("ProductImagesBaseUrl"), &awscdk.CfnOutputProps{
		Description: jsii.String("CloudFront base URL path for product images"),
		Value:       jsii.String("https://" + siteDomainName + "/" + productImagesKeyPrefix + "/"),
	})

	return siteResources{distribution: distribution, adminWebACL: adminWebACL}
}

func addObservability(stack awscdk.Stack, resources observabilityResources) {
	period5m := awscdk.Duration_Minutes(jsii.Number(5))
	period1d := awscdk.Duration_Days(jsii.Number(1))
	operationsAlarmTopic := addOperationsAlarmTopic(stack)

	apiRequests := resources.httpAPI.MetricCount(sumMetric("HTTP API requests", period5m, awscloudwatch.Unit_COUNT))
	api5xx := resources.httpAPI.MetricServerError(sumMetric("HTTP API 5xx", period5m, awscloudwatch.Unit_COUNT))
	apiLatencyP95 := resources.httpAPI.MetricLatency(percentileMetric("HTTP API p95 latency", period5m, awscloudwatch.Unit_MILLISECONDS, 95))
	apiIntegrationLatencyP95 := resources.httpAPI.MetricIntegrationLatency(percentileMetric("HTTP API integration p95 latency", period5m, awscloudwatch.Unit_MILLISECONDS, 95))

	cfRequests := resources.distribution.MetricRequests(sumMetric("CloudFront requests", period5m, awscloudwatch.Unit_COUNT))
	cf4xxRate := resources.distribution.Metric4xxErrorRate(avgMetric("CloudFront 4xx rate", period5m, awscloudwatch.Unit_PERCENT))
	cf5xxRate := resources.distribution.Metric5xxErrorRate(avgMetric("CloudFront 5xx rate", period5m, awscloudwatch.Unit_PERCENT))

	ssrInvocations := resources.ssrFunction.MetricInvocations(sumMetric("SSR invocations", period5m, awscloudwatch.Unit_COUNT))
	ssrErrors := resources.ssrFunction.MetricErrors(sumMetric("SSR errors", period5m, awscloudwatch.Unit_COUNT))
	ssrThrottles := resources.ssrFunction.MetricThrottles(sumMetric("SSR throttles", period5m, awscloudwatch.Unit_COUNT))
	ssrDurationP95 := resources.ssrFunction.MetricDuration(percentileMetric("SSR p95 duration", period5m, awscloudwatch.Unit_MILLISECONDS, 95))
	adminInvocations := resources.adminFunction.MetricInvocations(sumMetric("Admin invocations", period5m, awscloudwatch.Unit_COUNT))
	adminErrors := resources.adminFunction.MetricErrors(sumMetric("Admin errors", period5m, awscloudwatch.Unit_COUNT))
	adminThrottles := resources.adminFunction.MetricThrottles(sumMetric("Admin throttles", period5m, awscloudwatch.Unit_COUNT))
	adminDurationP95 := resources.adminFunction.MetricDuration(percentileMetric("Admin p95 duration", period5m, awscloudwatch.Unit_MILLISECONDS, 95))

	catalogDynamoOperations := []dynamoMetricOperation{
		{idSuffix: "get", dimension: "GetItem"},
		{idSuffix: "batchget", dimension: "BatchGetItem"},
		{idSuffix: "query", dimension: "Query"},
		{idSuffix: "scan", dimension: "Scan"},
		{idSuffix: "put", dimension: "PutItem"},
		{idSuffix: "delete", dimension: "DeleteItem"},
		{idSuffix: "update", dimension: "UpdateItem"},
		{idSuffix: "batchwrite", dimension: "BatchWriteItem"},
		{idSuffix: "transactwrite", dimension: "TransactWriteItems"},
	}
	adminLoginThrottleDynamoOperations := []dynamoMetricOperation{
		{idSuffix: "get", dimension: "GetItem"},
		{idSuffix: "update", dimension: "UpdateItem"},
		{idSuffix: "delete", dimension: "DeleteItem"},
	}
	commerceDynamoOperations := []dynamoMetricOperation{
		{idSuffix: "get", dimension: "GetItem"},
		{idSuffix: "query", dimension: "Query"},
		{idSuffix: "put", dimension: "PutItem"},
		{idSuffix: "delete", dimension: "DeleteItem"},
		{idSuffix: "update", dimension: "UpdateItem"},
		{idSuffix: "batchwrite", dimension: "BatchWriteItem"},
		{idSuffix: "transactwrite", dimension: "TransactWriteItems"},
	}
	catalogReadCapacity := resources.catalogTable.MetricConsumedReadCapacityUnits(sumMetric("Catalog read capacity", period5m, awscloudwatch.Unit_COUNT))
	catalogWriteCapacity := resources.catalogTable.MetricConsumedWriteCapacityUnits(sumMetric("Catalog write capacity", period5m, awscloudwatch.Unit_COUNT))
	catalogThrottles := dynamoOperationSumMetric(resources.catalogTable, "ThrottledRequests", "Catalog throttles", period5m, awscloudwatch.Unit_COUNT, "ct", catalogDynamoOperations)
	catalogSystemErrors := dynamoOperationSumMetric(resources.catalogTable, "SystemErrors", "Catalog system errors", period5m, awscloudwatch.Unit_COUNT, "cs", catalogDynamoOperations)
	adminLoginThrottleTableThrottles := dynamoOperationSumMetric(resources.adminLoginAttemptsTable, "ThrottledRequests", "Admin login throttle table throttles", period5m, awscloudwatch.Unit_COUNT, "alt", adminLoginThrottleDynamoOperations)
	commerceReadCapacity := resources.commerceTable.MetricConsumedReadCapacityUnits(sumMetric("Commerce read capacity", period5m, awscloudwatch.Unit_COUNT))
	commerceWriteCapacity := resources.commerceTable.MetricConsumedWriteCapacityUnits(sumMetric("Commerce write capacity", period5m, awscloudwatch.Unit_COUNT))
	commerceThrottles := dynamoOperationSumMetric(resources.commerceTable, "ThrottledRequests", "Commerce throttles", period5m, awscloudwatch.Unit_COUNT, "cmt", commerceDynamoOperations)
	commerceSystemErrors := dynamoOperationSumMetric(resources.commerceTable, "SystemErrors", "Commerce system errors", period5m, awscloudwatch.Unit_COUNT, "cme", commerceDynamoOperations)

	wafAllowed := wafMetric("AllowedRequests", "ALL", "WAF allowed requests", period5m)
	wafBlocked := wafMetric("BlockedRequests", "ALL", "WAF blocked requests", period5m)
	wafLoginBlocked := wafMetric("BlockedRequests", "ThailandGiftshopAdminCloudFrontLoginPost", "WAF login blocks", period5m)
	wafCustomerAuthBlocked := wafMetric("BlockedRequests", "ThailandGiftshopAdminCloudFrontCustomerAuthPost", "WAF customer auth blocks", period5m)

	adminLoginSuccess := appMetric(appobservability.MetricAdminLoginAttempt, "Admin login success", period5m, map[string]*string{"Service": jsii.String("admin"), "Operation": jsii.String("login"), "Outcome": jsii.String("success")})
	adminLoginInvalid := appMetric(appobservability.MetricAdminLoginAttempt, "Admin login invalid", period5m, map[string]*string{"Service": jsii.String("admin"), "Operation": jsii.String("login"), "Outcome": jsii.String("invalid")})
	adminLoginThrottled := appMetric(appobservability.MetricAdminLoginAttempt, "Admin login throttled", period5m, map[string]*string{"Service": jsii.String("admin"), "Operation": jsii.String("login"), "Outcome": jsii.String("throttled")})
	adminOriginRejected := appMetric(appobservability.MetricAdminOriginRejected, "Admin origin rejected", period5m, map[string]*string{"Service": jsii.String("admin"), "Operation": jsii.String("origin"), "Outcome": jsii.String("rejected")})
	catalogWriteSuccess := appMetric(appobservability.MetricCatalogWrite, "Catalog write success", period5m, map[string]*string{"Service": jsii.String("admin"), "Outcome": jsii.String("success")})
	catalogWriteErrors := appMetric(appobservability.MetricCatalogWrite, "Catalog write errors", period5m, map[string]*string{"Service": jsii.String("admin"), "Outcome": jsii.String("error")})
	imageUploadSuccess := appMetric(appobservability.MetricProductImageUpload, "Image upload success", period5m, map[string]*string{"Service": jsii.String("admin"), "Outcome": jsii.String("success")})
	imageUploadErrors := appMetric(appobservability.MetricProductImageUpload, "Image upload errors", period5m, map[string]*string{"Service": jsii.String("admin"), "Outcome": jsii.String("error")})

	// Customer auth, checkout, webhook, and stock metrics query the EMF
	// {Service, Outcome} rollup dimension set. Service values are part of the
	// emitting packages' contract: customer auth and webhook handling record
	// Service=ssr; checkout.Service and its stock adjustments record
	// Service=checkout.
	customerAuthSuccess := appMetric(appobservability.MetricCustomerAuth, "Customer auth success", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("success")})
	customerAuthInvalid := appMetric(appobservability.MetricCustomerAuth, "Customer auth invalid", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("invalid")})
	customerAuthThrottled := appMetric(appobservability.MetricCustomerAuth, "Customer auth throttled", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("throttled")})
	customerAuthError := appMetric(appobservability.MetricCustomerAuth, "Customer auth errors", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("error")})
	checkoutPaymentSuccess := appMetric(appobservability.MetricCheckoutPayment, "Checkout payment success", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("success")})
	checkoutPaymentInsufficientStock := appMetric(appobservability.MetricCheckoutPayment, "Checkout insufficient stock", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("insufficient_stock")})
	checkoutPaymentProviderError := appMetric(appobservability.MetricCheckoutPayment, "Checkout provider errors", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("provider_error")})
	checkoutPaymentError := appMetric(appobservability.MetricCheckoutPayment, "Checkout errors", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("error")})
	checkoutPaymentErrors := appOutcomeSumMetric(appobservability.MetricCheckoutPayment, "Checkout payment errors", period5m, metricServiceCheckout, "cp", []string{"provider_error", "error"})
	checkoutRefundIssued := appMetric(appobservability.MetricCheckoutRefund, "Refunds issued", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("issued")})
	checkoutRefundIssuedAuto := appMetric(appobservability.MetricCheckoutRefund, "Refunds auto-issued", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("issued_auto")})
	checkoutRefundSettled := appMetric(appobservability.MetricCheckoutRefund, "Refunds settled", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("settled")})
	checkoutRefundErrors := appOutcomeSumMetric(appobservability.MetricCheckoutRefund, "Checkout refund errors", period5m, metricServiceCheckout, "crf", []string{"failed", "provider_error", "error"})
	stripeWebhookProcessed := appMetric(appobservability.MetricStripeWebhook, "Stripe webhook processed", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("processed")})
	stripeWebhookIgnored := appMetric(appobservability.MetricStripeWebhook, "Stripe webhook ignored", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("ignored")})
	stripeWebhookErrors := appOutcomeSumMetric(appobservability.MetricStripeWebhook, "Stripe webhook errors", period5m, metricServiceSsr, "sw", []string{"invalid_signature", "amount_mismatch", "refund_mismatch", "paid_after_terminal", "error"})
	// StockAdjust is emitted by internal/catalog's AdjustStock (Service=catalog)
	// regardless of which Lambda triggered the adjustment.
	stockAdjustReserve := appMetric(appobservability.MetricStockAdjust, "Stock reservations", period5m, map[string]*string{"Service": jsii.String(metricServiceCatalog), "Outcome": jsii.String("reserve")})
	stockAdjustRelease := appMetric(appobservability.MetricStockAdjust, "Stock releases", period5m, map[string]*string{"Service": jsii.String(metricServiceCatalog), "Outcome": jsii.String("release")})
	stockAdjustConflict := appMetric(appobservability.MetricStockAdjust, "Stock adjust conflicts", period5m, map[string]*string{"Service": jsii.String(metricServiceCatalog), "Outcome": jsii.String("conflict")})
	stockAdjustRollbackErrors := appMetric(appobservability.MetricStockAdjust, "Stock adjust rollback errors", period5m, map[string]*string{"Service": jsii.String(metricServiceCatalog), "Outcome": jsii.String("rollback_error")})
	// OrderTransition and CommerceOperation carry no Outcome dimension, so
	// they have no {Service, Outcome} rollup; SEARCH expressions graph every
	// emitted dimension combination instead.
	orderTransitions := appSearchMetric(appobservability.MetricOrderTransition, "Order transitions", period5m, []string{"Actor", "From", "Service", "To"}, "Sum")
	commerceOperations := appSearchMetric(appobservability.MetricCommerceOperation, "Commerce store operations", period5m, []string{"Method", "Operation", "Service"}, "Sum")
	commerceOperationLatency := appSearchMetric(appobservability.MetricCommerceOperationMs, "Commerce store latency", period5m, []string{"Method", "Operation", "Service"}, "Average")

	productImagesBucketSize := s3StorageMetric(resources.productImagesBucket, "BucketSizeBytes", "Product image bytes", period1d, awscloudwatch.Unit_BYTES, "StandardStorage")
	productImagesObjectCount := s3StorageMetric(resources.productImagesBucket, "NumberOfObjects", "Product image objects", period1d, awscloudwatch.Unit_COUNT, "AllStorageTypes")

	alarms := []awscloudwatch.Alarm{
		addCriticalAlarm(stack, operationsAlarmTopic, "CloudFront5xxRateAlarm", "ThailandGiftshop-CloudFront-5xxRate-High", cf5xxRate, 5, 2, "CloudFront 5xx error rate is above 5%."),
		addCriticalAlarm(stack, operationsAlarmTopic, "HttpApi5xxAlarm", "ThailandGiftshop-HttpApi-5xx-High", api5xx, 5, 2, "HTTP API is returning elevated 5xx responses."),
		addAlarm(stack, "HttpApiLatencyAlarm", "ThailandGiftshop-HttpApi-LatencyP95-High", apiLatencyP95, 3000, 3, "HTTP API p95 latency is above 3 seconds."),
		addCriticalAlarm(stack, operationsAlarmTopic, "SsrLambdaErrorsAlarm", "ThailandGiftshop-SsrLambda-Errors", ssrErrors, 0, 2, "SSR Lambda has errors."),
		addCriticalAlarm(stack, operationsAlarmTopic, "AdminLambdaErrorsAlarm", "ThailandGiftshop-AdminLambda-Errors", adminErrors, 0, 2, "Admin Lambda has errors."),
		addAlarm(stack, "SsrLambdaThrottlesAlarm", "ThailandGiftshop-SsrLambda-Throttles", ssrThrottles, 0, 1, "SSR Lambda is throttling."),
		addAlarm(stack, "AdminLambdaThrottlesAlarm", "ThailandGiftshop-AdminLambda-Throttles", adminThrottles, 0, 1, "Admin Lambda is throttling."),
		addAlarm(stack, "CatalogThrottlesAlarm", "ThailandGiftshop-CatalogTable-Throttles", catalogThrottles, 0, 1, "Catalog DynamoDB table is throttling."),
		addAlarm(stack, "CatalogSystemErrorsAlarm", "ThailandGiftshop-CatalogTable-SystemErrors", catalogSystemErrors, 0, 1, "Catalog DynamoDB table has system errors."),
		// The alarm ID and name predate the customer-auth rate rule; both are
		// kept stable so the alarm resource is not replaced on deploy. The
		// Rule=ALL metric covers every edge rule in the web ACL.
		addAlarm(stack, "WafAdminBlocksAlarm", "ThailandGiftshop-WAF-AdminBlocks", wafBlocked, 10, 1, "WAF blocked requests across all edge rules (admin login, admin path, and customer auth) exceeded the normal operating threshold."),
		addCriticalAlarm(stack, operationsAlarmTopic, "AdminOriginRejectedAlarm", "ThailandGiftshop-Admin-OriginRejected", adminOriginRejected, 0, 1, "Admin origin header rejections were observed."),
		addAlarm(stack, "AdminLoginInvalidAlarm", "ThailandGiftshop-Admin-InvalidLogins", adminLoginInvalid, 10, 1, "Admin invalid login attempts exceeded the normal operating threshold."),
		addAlarm(stack, "AdminLoginThrottledAlarm", "ThailandGiftshop-Admin-ThrottledLogins", adminLoginThrottled, 0, 1, "Admin login throttling occurred."),
		addCriticalAlarm(stack, operationsAlarmTopic, "CatalogWriteErrorsAlarm", "ThailandGiftshop-CatalogWrite-Errors", catalogWriteErrors, 0, 1, "Admin catalog write errors occurred."),
		addCriticalAlarm(stack, operationsAlarmTopic, "ImageUploadErrorsAlarm", "ThailandGiftshop-ProductImageUpload-Errors", imageUploadErrors, 0, 1, "Product image upload errors occurred."),
		addAlarm(stack, "AdminLoginThrottleTableThrottlesAlarm", "ThailandGiftshop-AdminLoginThrottleTable-Throttles", adminLoginThrottleTableThrottles, 0, 1, "Admin login throttle DynamoDB table is throttling."),
		addCriticalAlarm(stack, operationsAlarmTopic, "StripeWebhookErrorsAlarm", "ThailandGiftshop-StripeWebhook-Errors", stripeWebhookErrors, 0, 1, "Stripe webhook signature failures, amount or refund mismatches, payments captured for terminal orders (auto-refund already issued — verify its legitimacy and settlement in Stripe), or processing errors occurred."),
		addCriticalAlarm(stack, operationsAlarmTopic, "CheckoutPaymentErrorsAlarm", "ThailandGiftshop-CheckoutPayment-Errors", checkoutPaymentErrors, 0, 1, "Checkout payment provider or processing errors occurred."),
		addCriticalAlarm(stack, operationsAlarmTopic, "CheckoutRefundErrorsAlarm", "ThailandGiftshop-CheckoutRefund-Errors", checkoutRefundErrors, 0, 1, "A Stripe refund failed, could not be issued, or hit a processing error; the affected order page in /admin/orders names the refund and failure reason."),
		addCriticalAlarm(stack, operationsAlarmTopic, "StockAdjustRollbackErrorsAlarm", "ThailandGiftshop-StockAdjust-RollbackErrors", stockAdjustRollbackErrors, 0, 1, "A stock reservation rollback failed; product stock may need manual correction."),
	}

	dashboard := awscloudwatch.NewDashboard(stack, jsii.String("OperationsDashboard"), &awscloudwatch.DashboardProps{
		DashboardName:   jsii.String(operationsDashboardName),
		DefaultInterval: awscdk.Duration_Hours(jsii.Number(6)),
	})
	dashboard.AddWidgets(awscloudwatch.NewTextWidget(&awscloudwatch.TextWidgetProps{
		Markdown: jsii.String("# Thailand Gift Shop operations\nProduction service metrics for https://" + siteDomainName + " in us-east-1. Critical alarm actions publish to SNS; watchlist alarms remain dashboard-only."),
		Width:    jsii.Number(24),
		Height:   jsii.Number(2),
	}))
	dashboard.AddWidgets(
		awscloudwatch.NewSingleValueWidget(&awscloudwatch.SingleValueWidgetProps{
			Title:   jsii.String("Current volume and errors"),
			Width:   jsii.Number(8),
			Height:  jsii.Number(4),
			Metrics: cwMetrics(cfRequests, apiRequests, cf5xxRate, api5xx),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Public traffic and edge errors"),
			Width: jsii.Number(16),
			Left:  cwMetrics(cfRequests, apiRequests),
			Right: cwMetrics(cf4xxRate, cf5xxRate),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("API and Lambda latency"),
			Width: jsii.Number(12),
			Left:  cwMetrics(apiLatencyP95, apiIntegrationLatencyP95, ssrDurationP95, adminDurationP95),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Lambda health"),
			Width: jsii.Number(12),
			Left:  cwMetrics(ssrInvocations, adminInvocations),
			Right: cwMetrics(ssrErrors, adminErrors, ssrThrottles, adminThrottles),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Admin and WAF security"),
			Width: jsii.Number(12),
			Left:  cwMetrics(wafAllowed, wafBlocked, wafLoginBlocked, wafCustomerAuthBlocked),
			Right: cwMetrics(adminLoginSuccess, adminLoginInvalid, adminLoginThrottled, adminOriginRejected),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Catalog and DynamoDB health"),
			Width: jsii.Number(12),
			Left:  cwMetrics(catalogWriteSuccess, catalogWriteErrors, catalogReadCapacity, catalogWriteCapacity),
			Right: cwMetrics(catalogThrottles, catalogSystemErrors, adminLoginThrottleTableThrottles),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Customer accounts and checkout"),
			Width: jsii.Number(12),
			Left:  cwMetrics(customerAuthSuccess, customerAuthInvalid, customerAuthThrottled, customerAuthError),
			Right: cwMetrics(checkoutPaymentSuccess, checkoutPaymentInsufficientStock, checkoutPaymentProviderError, checkoutPaymentError, checkoutRefundIssued, checkoutRefundIssuedAuto, checkoutRefundSettled, checkoutRefundErrors),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Stripe webhooks and stock adjustments"),
			Width: jsii.Number(12),
			Left:  cwMetrics(stripeWebhookProcessed, stripeWebhookIgnored, stripeWebhookErrors),
			Right: cwMetrics(stockAdjustReserve, stockAdjustRelease, stockAdjustConflict, stockAdjustRollbackErrors),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Commerce table and order activity"),
			Width: jsii.Number(12),
			Left:  cwMetrics(commerceReadCapacity, commerceWriteCapacity, commerceThrottles, commerceSystemErrors),
			Right: cwMetrics(orderTransitions),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Commerce store operations"),
			Width: jsii.Number(12),
			Left:  cwMetrics(commerceOperations),
			Right: cwMetrics(commerceOperationLatency),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Product image uploads and storage"),
			Width: jsii.Number(12),
			Left:  cwMetrics(imageUploadSuccess, imageUploadErrors),
			Right: cwMetrics(productImagesBucketSize, productImagesObjectCount),
		}),
		awscloudwatch.NewSingleValueWidget(&awscloudwatch.SingleValueWidgetProps{
			Title:   jsii.String("Alarm watchlist"),
			Width:   jsii.Number(12),
			Height:  jsii.Number(4),
			Metrics: cwMetrics(alarmMetrics(alarms...)...),
		}),
	)
}

func addOperationsAlarmTopic(stack awscdk.Stack) awssns.Topic {
	alarmNotificationEmail := awscdk.NewCfnParameter(stack, jsii.String("AlarmNotificationEmail"), &awscdk.CfnParameterProps{
		Description: jsii.String("Email address subscribed to critical operations alarms"),
		Type:        jsii.String("String"),
	})
	topic := awssns.NewTopic(stack, jsii.String("OperationsAlarmTopic"), &awssns.TopicProps{
		TopicName: jsii.String(operationsAlarmTopicName),
	})
	topic.AddSubscription(awssnssubscriptions.NewEmailSubscription(alarmNotificationEmail.ValueAsString(), nil))

	return topic
}

func sumMetric(label string, period awscdk.Duration, unit awscloudwatch.Unit) *awscloudwatch.MetricOptions {
	return metricOptions(label, period, awscloudwatch.Stats_SUM(), unit)
}

func avgMetric(label string, period awscdk.Duration, unit awscloudwatch.Unit) *awscloudwatch.MetricOptions {
	return metricOptions(label, period, awscloudwatch.Stats_AVERAGE(), unit)
}

func percentileMetric(label string, period awscdk.Duration, unit awscloudwatch.Unit, percentile float64) *awscloudwatch.MetricOptions {
	return metricOptions(label, period, awscloudwatch.Stats_P(jsii.Number(percentile)), unit)
}

func metricOptions(label string, period awscdk.Duration, statistic *string, unit awscloudwatch.Unit) *awscloudwatch.MetricOptions {
	return &awscloudwatch.MetricOptions{
		Label:     jsii.String(label),
		Period:    period,
		Statistic: statistic,
		Unit:      unit,
	}
}

func dynamoOperationSumMetric(table awsdynamodb.ITable, metricName string, label string, period awscdk.Duration, unit awscloudwatch.Unit, idPrefix string, operations []dynamoMetricOperation) awscloudwatch.MathExpression {
	usingMetrics := make(map[string]awscloudwatch.IMetric, len(operations))
	expressionTerms := make([]string, 0, len(operations))
	for _, operation := range operations {
		id := idPrefix + operation.idSuffix
		expressionTerms = append(expressionTerms, id)
		usingMetrics[id] = awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
			Namespace:  jsii.String("AWS/DynamoDB"),
			MetricName: jsii.String(metricName),
			DimensionsMap: &map[string]*string{
				"TableName": table.TableName(),
				"Operation": jsii.String(operation.dimension),
			},
			Label:     jsii.String(operation.dimension),
			Period:    period,
			Statistic: awscloudwatch.Stats_SUM(),
			Unit:      unit,
		})
	}

	return awscloudwatch.NewMathExpression(&awscloudwatch.MathExpressionProps{
		Expression:   jsii.String(strings.Join(expressionTerms, "+")),
		Label:        jsii.String(label),
		Period:       period,
		UsingMetrics: &usingMetrics,
	})
}

func appMetric(metricName string, label string, period awscdk.Duration, dimensions map[string]*string) awscloudwatch.Metric {
	return awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
		Namespace:     jsii.String(appobservability.Namespace),
		MetricName:    jsii.String(metricName),
		DimensionsMap: &dimensions,
		Label:         jsii.String(label),
		Period:        period,
		Statistic:     awscloudwatch.Stats_SUM(),
		Unit:          awscloudwatch.Unit_COUNT,
	})
}

// appOutcomeSumMetric sums one app metric across several Outcome dimension
// values for a single Service — the alarmable equivalent of graphing each
// outcome separately (CloudWatch metrics cannot OR dimension values).
func appOutcomeSumMetric(metricName string, label string, period awscdk.Duration, service string, idPrefix string, outcomes []string) awscloudwatch.MathExpression {
	usingMetrics := make(map[string]awscloudwatch.IMetric, len(outcomes))
	expressionTerms := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		id := idPrefix + outcome
		expressionTerms = append(expressionTerms, id)
		usingMetrics[id] = appMetric(metricName, metricName+" "+outcome, period, map[string]*string{
			"Service": jsii.String(service),
			"Outcome": jsii.String(outcome),
		})
	}

	return awscloudwatch.NewMathExpression(&awscloudwatch.MathExpressionProps{
		Expression:   jsii.String(strings.Join(expressionTerms, "+")),
		Label:        jsii.String(label),
		Period:       period,
		UsingMetrics: &usingMetrics,
	})
}

// appSearchMetric graphs every emitted dimension combination of an app metric
// whose full EMF dimension set carries no {Service, Outcome} rollup. SEARCH
// expressions are dashboard-only — they cannot back alarms.
func appSearchMetric(metricName string, label string, period awscdk.Duration, dimensionNames []string, statistic string) awscloudwatch.MathExpression {
	schemaTerms := make([]string, 0, len(dimensionNames)+1)
	schemaTerms = append(schemaTerms, appobservability.Namespace)
	for _, dimensionName := range dimensionNames {
		schemaTerms = append(schemaTerms, dimensionName)
	}

	schema := strings.Join(schemaTerms, ",")
	return awscloudwatch.NewMathExpression(&awscloudwatch.MathExpressionProps{
		Expression: jsii.String(`SEARCH('{` + schema + `} MetricName="` + metricName + `"', '` + statistic + `', 300)`),
		Label:      jsii.String(label),
		Period:     period,
	})
}

func wafMetric(metricName string, rule string, label string, period awscdk.Duration) awscloudwatch.Metric {
	return awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
		Namespace:  jsii.String("AWS/WAFV2"),
		MetricName: jsii.String(metricName),
		DimensionsMap: &map[string]*string{
			"Region": jsii.String("Global"),
			"Rule":   jsii.String(rule),
			"WebACL": jsii.String("ThailandGiftshopAdminCloudFront"),
		},
		Label:     jsii.String(label),
		Period:    period,
		Statistic: awscloudwatch.Stats_SUM(),
		Unit:      awscloudwatch.Unit_COUNT,
		Region:    jsii.String("us-east-1"),
	})
}

func s3StorageMetric(bucket awss3.IBucket, metricName string, label string, period awscdk.Duration, unit awscloudwatch.Unit, storageType string) awscloudwatch.Metric {
	return awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
		Namespace:  jsii.String("AWS/S3"),
		MetricName: jsii.String(metricName),
		DimensionsMap: &map[string]*string{
			"BucketName":  bucket.BucketName(),
			"StorageType": jsii.String(storageType),
		},
		Label:     jsii.String(label),
		Period:    period,
		Statistic: awscloudwatch.Stats_AVERAGE(),
		Unit:      unit,
	})
}

func addAlarm(stack awscdk.Stack, id string, name string, metric awscloudwatch.IMetric, threshold float64, evaluationPeriods float64, description string) awscloudwatch.Alarm {
	return newAlarm(stack, id, name, metric, threshold, evaluationPeriods, description, false)
}

func addCriticalAlarm(stack awscdk.Stack, operationsAlarmTopic awssns.ITopic, id string, name string, metric awscloudwatch.IMetric, threshold float64, evaluationPeriods float64, description string) awscloudwatch.Alarm {
	alarm := newAlarm(stack, id, name, metric, threshold, evaluationPeriods, description, true)
	alarm.AddAlarmAction(awscloudwatchactions.NewSnsAction(operationsAlarmTopic))

	return alarm
}

func newAlarm(stack awscdk.Stack, id string, name string, metric awscloudwatch.IMetric, threshold float64, evaluationPeriods float64, description string, actionsEnabled bool) awscloudwatch.Alarm {
	return awscloudwatch.NewAlarm(stack, jsii.String(id), &awscloudwatch.AlarmProps{
		ActionsEnabled:     jsii.Bool(actionsEnabled),
		AlarmDescription:   jsii.String(description),
		AlarmName:          jsii.String(name),
		ComparisonOperator: awscloudwatch.ComparisonOperator_GREATER_THAN_THRESHOLD,
		EvaluationPeriods:  jsii.Number(evaluationPeriods),
		Metric:             metric,
		Threshold:          jsii.Number(threshold),
		TreatMissingData:   awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
}

func cwMetrics(metrics ...awscloudwatch.IMetric) *[]awscloudwatch.IMetric {
	return &metrics
}

func alarmMetrics(alarms ...awscloudwatch.Alarm) []awscloudwatch.IMetric {
	metrics := make([]awscloudwatch.IMetric, 0, len(alarms))
	for _, alarm := range alarms {
		metrics = append(metrics, alarm.Metric())
	}
	return metrics
}

func siteHostedZone(stack awscdk.Stack) awsroute53.IHostedZone {
	hostedZoneID := awscdk.NewCfnParameter(stack, jsii.String("HostedZoneId"), &awscdk.CfnParameterProps{
		Description: jsii.String("Route 53 public hosted zone ID for thailandgiftshop.com"),
		Type:        jsii.String("String"),
	})

	return awsroute53.HostedZone_FromHostedZoneAttributes(stack, jsii.String("SiteHostedZone"), &awsroute53.HostedZoneAttributes{
		HostedZoneId: hostedZoneID.ValueAsString(),
		ZoneName:     jsii.String(siteDomainName),
	})
}

func siteSecurityHeaders(stack awscdk.Stack) awscloudfront.ResponseHeadersPolicy {
	return awscloudfront.NewResponseHeadersPolicy(stack, jsii.String("SiteSecurityHeadersPolicy"), &awscloudfront.ResponseHeadersPolicyProps{
		Comment: jsii.String("Security headers for thailandgiftshop.com"),
		SecurityHeadersBehavior: &awscloudfront.ResponseSecurityHeadersBehavior{
			ContentSecurityPolicy: &awscloudfront.ResponseHeadersContentSecurityPolicy{
				ContentSecurityPolicy: jsii.String("default-src 'self'; base-uri 'self'; connect-src 'self' https://*.s3.amazonaws.com https://*.s3.us-east-1.amazonaws.com; frame-ancestors 'none'; form-action 'self' https://*.s3.amazonaws.com https://*.s3.us-east-1.amazonaws.com https://checkout.stripe.com; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'; manifest-src 'self'"),
				Override:              jsii.Bool(true),
			},
			ContentTypeOptions: &awscloudfront.ResponseHeadersContentTypeOptions{
				Override: jsii.Bool(true),
			},
			FrameOptions: &awscloudfront.ResponseHeadersFrameOptions{
				FrameOption: awscloudfront.HeadersFrameOption_DENY,
				Override:    jsii.Bool(true),
			},
			ReferrerPolicy: &awscloudfront.ResponseHeadersReferrerPolicy{
				Override:       jsii.Bool(true),
				ReferrerPolicy: awscloudfront.HeadersReferrerPolicy_STRICT_ORIGIN_WHEN_CROSS_ORIGIN,
			},
			StrictTransportSecurity: &awscloudfront.ResponseHeadersStrictTransportSecurity{
				AccessControlMaxAge: awscdk.Duration_Days(jsii.Number(365)),
				Override:            jsii.Bool(true),
				Preload:             jsii.Bool(false),
			},
		},
	})
}

func addSiteAliasRecords(stack awscdk.Stack, hostedZone awsroute53.IHostedZone, distribution awscloudfront.IDistribution) {
	awsroute53.NewARecord(stack, jsii.String("SiteARecord"), &awsroute53.ARecordProps{
		Target: awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(distribution)),
		Zone:   hostedZone,
	})
	awsroute53.NewAaaaRecord(stack, jsii.String("SiteAAAARecord"), &awsroute53.AaaaRecordProps{
		Target: awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(distribution)),
		Zone:   hostedZone,
	})
	awsroute53.NewARecord(stack, jsii.String("WwwARecord"), &awsroute53.ARecordProps{
		RecordName: jsii.String("www"),
		Target:     awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(distribution)),
		Zone:       hostedZone,
	})
	awsroute53.NewAaaaRecord(stack, jsii.String("WwwAAAARecord"), &awsroute53.AaaaRecordProps{
		RecordName: jsii.String("www"),
		Target:     awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(distribution)),
		Zone:       hostedZone,
	})
}

func ssrOrigin(httpAPI awsapigatewayv2.HttpApi, adminOriginHeaderSecret awssecretsmanager.ISecret) awscloudfront.IOrigin {
	return awscloudfrontorigins.NewHttpOrigin(apiGatewayDomainName(httpAPI), &awscloudfrontorigins.HttpOriginProps{
		CustomHeaders: &map[string]*string{
			"X-TGS-Origin-Secret": adminOriginHeaderSecretReference(adminOriginHeaderSecret),
		},
		ProtocolPolicy: awscloudfront.OriginProtocolPolicy_HTTPS_ONLY,
	})
}

func apiGatewayDomainName(httpAPI awsapigatewayv2.HttpApi) *string {
	return awscdk.Fn_Join(jsii.String(""), &[]*string{
		httpAPI.HttpApiId(),
		jsii.String(".execute-api."),
		awscdk.Aws_REGION(),
		jsii.String("."),
		awscdk.Aws_URL_SUFFIX(),
	})
}

func main() {
	defer jsii.Close()

	app := awscdk.NewApp(nil)

	// The construct ID is used as the default CloudFormation stack name.
	NewThailandGiftshopStack(app, "ThailandGiftshopStack", &ThailandGiftshopStackProps{
		StackProps: awscdk.StackProps{
			Description: jsii.String("CDK stack for thailandgiftshop.com"),
			Env:         env(),
		},
	})

	app.Synth(nil)
}

func env() *awscdk.Environment {
	account := os.Getenv("CDK_DEFAULT_ACCOUNT")
	region := configuredRegion()
	enforceProductionRegion()

	if account == "" && region == "" {
		return nil
	}

	return &awscdk.Environment{
		Account: stringOrNil(account),
		Region:  stringOrNil(region),
	}
}

func validateStackRegion(stackProps awscdk.StackProps) {
	if stackProps.Env == nil || stackProps.Env.Region == nil || *stackProps.Env.Region == "" {
		return
	}
	if *stackProps.Env.Region != productionRegion {
		panic("ThailandGiftshopStack must be synthesized in " + productionRegion + " when a concrete region is configured, got " + *stackProps.Env.Region)
	}
}

func configuredRegion() string {
	if region := os.Getenv("CDK_DEFAULT_REGION"); region != "" {
		return region
	}

	return os.Getenv("AWS_REGION")
}

func enforceProductionRegion() {
	for _, envVar := range []struct {
		name  string
		value string
	}{
		{name: "AWS_REGION", value: os.Getenv("AWS_REGION")},
		{name: "CDK_DEFAULT_REGION", value: os.Getenv("CDK_DEFAULT_REGION")},
	} {
		if envVar.value != "" && envVar.value != productionRegion {
			panic(envVar.name + " must be " + productionRegion + " for production deploys, got " + envVar.value)
		}
	}
}

func stringOrNil(value string) *string {
	if value == "" {
		return nil
	}

	return jsii.String(value)
}
