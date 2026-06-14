package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/jsii-runtime-go"
	"os"
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
	ssrLambdaLogGroupName       = "/aws/lambda/thailandgiftshop-ssr"
	ssrLambdaAliasName          = "live"
	// Keep at 0 until the account concurrency quota can leave Lambda's
	// required 10 unreserved executions available.
	ssrProvisionedConcurrency                    = 0
	adminLambdaLogGroupName                      = "/aws/lambda/thailandgiftshop-admin"
	adminLoginAttemptsTableName                  = "thailandgiftshop-admin-login-attempts"
	adminLoginAttemptsPKName                     = "client_key"
	adminLoginAttemptsTTLName                    = "expires_at"
	adminPreviousOriginHeaderSecretParameterName = "AdminOriginHeaderPreviousSecret"
	adminOriginHeaderVersionParameterName        = "AdminOriginHeaderVersion"
	lambdaMemorySizeMB                           = 128
	lambdaHighMemoryUsedMB                       = 100
	lambdaNearTimeoutDurationMs                  = 8000
	lambdaReportMetricNamespace                  = "ThailandGiftshop/Lambda"

	ssrOriginRequestPolicyName = "thailandgiftshop-ssr-origin"
	ssrCachePolicyName         = "thailandgiftshop-ssr-cache"
	skipLinkStyleCSPHash       = "'sha256-rE+bFBvY9ntOvyDDnI4OBE9+BdhsJpyJJWxGWylO5oY='"

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
