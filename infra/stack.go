package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

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
	adminPreviousOriginHeaderSecret := adminPreviousOriginHeaderSecretValue(stack)
	adminOriginHeaderVersion := adminOriginHeaderVersionValue(stack)
	hostedZone := siteHostedZone(stack)
	emailIdentity := addEmailIdentity(stack, hostedZone)
	ssr := addSSR(stack, catalogTable, commerceTable, productImagesBucket, adminLoginAttemptsTable, adminOriginHeaderSecret, adminPreviousOriginHeaderSecret, adminOriginHeaderVersion, emailIdentity)
	site := addSite(stack, ssr.httpAPI, productImagesBucket, adminOriginHeaderSecret, hostedZone)
	addObservability(stack, observabilityResources{
		catalogTable:            catalogTable,
		commerceTable:           commerceTable,
		productImagesBucket:     productImagesBucket,
		adminLoginAttemptsTable: adminLoginAttemptsTable,
		ssrFunction:             ssr.function,
		ssrLogGroup:             ssr.logGroup,
		adminFunction:           ssr.admin.function,
		adminLogGroup:           ssr.admin.logGroup,
		httpAPI:                 ssr.httpAPI,
		distribution:            site.distribution,
		adminWebACL:             site.adminWebACL,
	})

	return stack
}
