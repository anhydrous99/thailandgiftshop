package main

import (
	adminauth "github.com/anhydrous99/thailandgiftshop/internal/admin"
	appenv "github.com/anhydrous99/thailandgiftshop/internal/appenv"
	cartsession "github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	"github.com/anhydrous99/thailandgiftshop/internal/location"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2integrations"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssecretsmanager"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsses"
	"github.com/aws/jsii-runtime-go"
)

func addSSR(stack awscdk.Stack, catalogTable awsdynamodb.ITable, commerceTable awsdynamodb.ITable, productImagesBucket awss3.IBucket, adminLoginAttemptsTable awsdynamodb.ITable, adminOriginHeaderSecret awssecretsmanager.ISecret, adminPreviousOriginHeaderSecret *string, adminOriginHeaderVersion *string, emailIdentity awsses.IEmailIdentity) ssrResources {
	lambdaLogGroup := awslogs.NewLogGroup(stack, jsii.String("SsrLambdaLogGroup"), &awslogs.LogGroupProps{
		LogGroupName: jsii.String(ssrLambdaLogGroupName),
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
		CurrentVersionOptions: &awslambda.VersionOptions{
			Description:   awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("SSR origin header version "), adminOriginHeaderVersion}),
			RemovalPolicy: awscdk.RemovalPolicy_DESTROY,
		},
		Description: jsii.String("Server-side HTML renderer for thailandgiftshop.com"),
		Environment: &map[string]*string{
			catalog.EnvTableName:                         catalogTable.TableName(),
			catalog.EnvSlugIndexName:                     jsii.String(catalog.DefaultSlugIndexName),
			catalog.EnvPublicIndexName:                   jsii.String(catalog.DefaultPublicIndexName),
			catalog.EnvRecentIndexName:                   jsii.String(catalog.DefaultRecentIndexName),
			catalog.EnvEntityIndexName:                   jsii.String(catalog.DefaultEntityIndexName),
			catalog.EnvProductImagePlaceholderURL:        jsii.String(catalog.DefaultProductImagePlaceholderURL),
			appenv.EnvAppEnvironment:                     jsii.String(appenv.EnvironmentProduction),
			cartsession.EnvCookieSecret:                  cartCookieSecretReference(cartCookieSecret),
			adminauth.EnvAdminOriginHeaderSecret:         adminOriginHeaderSecretReference(adminOriginHeaderSecret),
			adminauth.EnvAdminPreviousOriginHeaderSecret: adminPreviousOriginHeaderSecret,
			commerce.EnvTableName:                        commerceTable.TableName(),
			commerce.EnvCustomerOrdersIndexName:          jsii.String(commerce.DefaultCustomerOrdersIndexName),
			commerce.EnvOrdersIndexName:                  jsii.String(commerce.DefaultOrdersIndexName),
			commerce.EnvSessionSecret:                    customerSessionSecretReference(customerSessionSecret),
			payments.EnvStripeCredentialsSecretName:      jsii.String(stripeCredentialsSecretName),
			payments.EnvPublicBaseURL:                    jsii.String(payments.DefaultPublicBaseURL),
			email.EnvSenderMode:                          jsii.String(email.SenderKindSES),
			email.EnvFromAddress:                         jsii.String(email.DefaultFromAddress),
			email.EnvSESRegion:                           jsii.String(productionRegion),
			location.EnvValidatorMode:                    jsii.String(location.ModeALS),
		},
		FunctionName: jsii.String("thailandgiftshop-ssr"),
		Handler:      jsii.String("bootstrap"),
		LogGroup:     lambdaLogGroup,
		MemorySize:   jsii.Number(lambdaMemorySizeMB),
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
	grantEmailSend(ssrFunction, emailIdentity)
	grantAddressValidation(ssrFunction)
	ssrFunction.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("dynamodb:TransactWriteItems"),
		},
		Resources: &[]*string{
			commerceTable.TableArn(),
		},
	}))
	ssrAliasOptions := &awslambda.AliasOptions{}
	if ssrProvisionedConcurrency > 0 {
		ssrAliasOptions.ProvisionedConcurrentExecutions = jsii.Number(ssrProvisionedConcurrency)
	}
	ssrAlias := ssrFunction.AddAlias(jsii.String(ssrLambdaAliasName), ssrAliasOptions)

	httpAPI := awsapigatewayv2.NewHttpApi(stack, jsii.String("SsrHttpApi"), &awsapigatewayv2.HttpApiProps{
		ApiName:            jsii.String("thailandgiftshop-ssr"),
		CreateDefaultStage: jsii.Bool(false),
		Description:        jsii.String("HTTP API for thailandgiftshop.com HTML rendering"),
	})

	defaultRoute := awsapigatewayv2.NewHttpRoute(stack, jsii.String("SsrDefaultRoute"), &awsapigatewayv2.HttpRouteProps{
		HttpApi:  httpAPI,
		RouteKey: awsapigatewayv2.HttpRouteKey_DEFAULT(),
		Integration: awsapigatewayv2integrations.NewHttpLambdaIntegration(jsii.String("SsrLambdaIntegration"), ssrAlias, &awsapigatewayv2integrations.HttpLambdaIntegrationProps{
			PayloadFormatVersion: awsapigatewayv2.PayloadFormatVersion_VERSION_2_0(),
			Timeout:              awscdk.Duration_Seconds(jsii.Number(10)),
		}),
	})
	admin := addAdmin(stack, catalogTable, commerceTable, productImagesBucket, adminLoginAttemptsTable, adminOriginHeaderSecret, adminPreviousOriginHeaderSecret, customerSessionSecret, stripeSecret, emailIdentity, httpAPI)

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

	return ssrResources{httpAPI: httpAPI, function: ssrFunction, logGroup: lambdaLogGroup, admin: admin}
}

func addAdmin(stack awscdk.Stack, catalogTable awsdynamodb.ITable, commerceTable awsdynamodb.ITable, productImagesBucket awss3.IBucket, adminLoginAttemptsTable awsdynamodb.ITable, adminOriginHeaderSecret awssecretsmanager.ISecret, adminPreviousOriginHeaderSecret *string, customerSessionSecret awssecretsmanager.ISecret, stripeSecret awssecretsmanager.ISecret, emailIdentity awsses.IEmailIdentity, httpAPI awsapigatewayv2.HttpApi) adminResources {
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
			catalog.EnvTableName:                         catalogTable.TableName(),
			catalog.EnvSlugIndexName:                     jsii.String(catalog.DefaultSlugIndexName),
			catalog.EnvPublicIndexName:                   jsii.String(catalog.DefaultPublicIndexName),
			catalog.EnvRecentIndexName:                   jsii.String(catalog.DefaultRecentIndexName),
			catalog.EnvEntityIndexName:                   jsii.String(catalog.DefaultEntityIndexName),
			adminauth.EnvProductImagesBucketName:         productImagesBucket.BucketName(),
			adminauth.EnvProductImagesKeyPrefix:          jsii.String(productImagesKeyPrefix),
			catalog.EnvProductImagePlaceholderURL:        jsii.String(catalog.DefaultProductImagePlaceholderURL),
			appenv.EnvAppEnvironment:                     jsii.String(appenv.EnvironmentProduction),
			adminauth.EnvAdminCredentialsSecretName:      jsii.String(adminCredentialsSecretName),
			adminauth.EnvAdminLoginAttemptsTableName:     adminLoginAttemptsTable.TableName(),
			adminauth.EnvAdminOriginHeaderSecret:         adminOriginHeaderSecretReference(adminOriginHeaderSecret),
			adminauth.EnvAdminPreviousOriginHeaderSecret: adminPreviousOriginHeaderSecret,
			commerce.EnvTableName:                        commerceTable.TableName(),
			commerce.EnvCustomerOrdersIndexName:          jsii.String(commerce.DefaultCustomerOrdersIndexName),
			commerce.EnvOrdersIndexName:                  jsii.String(commerce.DefaultOrdersIndexName),
			commerce.EnvSessionSecret:                    customerSessionSecretReference(customerSessionSecret),
			// Admin cancels of pending orders verify (and expire) the order's
			// Stripe checkout session before releasing stock; without the
			// credentials the handler skips that session-expiry guard.
			payments.EnvStripeCredentialsSecretName: jsii.String(stripeCredentialsSecretName),
			email.EnvSenderMode:                     jsii.String(email.SenderKindSES),
			email.EnvFromAddress:                    jsii.String(email.DefaultFromAddress),
			email.EnvSESRegion:                      jsii.String(productionRegion),
		},
		FunctionName: jsii.String("thailandgiftshop-admin"),
		Handler:      jsii.String("bootstrap"),
		LogGroup:     lambdaLogGroup,
		MemorySize:   jsii.Number(lambdaMemorySizeMB),
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
	grantEmailSend(adminFunction, emailIdentity)
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

	return adminResources{routes: []awsapigatewayv2.HttpRoute{exactRoute, proxyRoute}, function: adminFunction, logGroup: lambdaLogGroup}
}
