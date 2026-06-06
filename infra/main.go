package main

import (
	"os"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2integrations"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscertificatemanager"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfront"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfrontorigins"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53targets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3deployment"
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

	staticAssetsKeyPrefix  = "static"
	productImagesKeyPrefix = "images"
)

type ThailandGiftshopStackProps struct {
	awscdk.StackProps
}

func NewThailandGiftshopStack(scope constructs.Construct, id string, props *ThailandGiftshopStackProps) awscdk.Stack {
	var stackProps awscdk.StackProps
	if props != nil {
		stackProps = props.StackProps
	}

	stack := awscdk.NewStack(scope, &id, &stackProps)

	awscdk.Tags_Of(stack).Add(jsii.String("Project"), jsii.String("thailandgiftshop"), nil)
	awscdk.Tags_Of(stack).Add(jsii.String("ManagedBy"), jsii.String("aws-cdk"), nil)

	catalogTable := addCatalog(stack)
	httpAPI := addSSR(stack, catalogTable)
	addSite(stack, httpAPI)

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

func addSSR(stack awscdk.Stack, catalogTable awsdynamodb.ITable) awsapigatewayv2.HttpApi {
	lambdaLogGroup := awslogs.NewLogGroup(stack, jsii.String("SsrLambdaLogGroup"), &awslogs.LogGroupProps{
		LogGroupName: jsii.String("/aws/lambda/thailandgiftshop-ssr"),
		Retention:    awslogs.RetentionDays_THREE_MONTHS,
	})

	ssrFunction := awslambda.NewFunction(stack, jsii.String("SsrLambda"), &awslambda.FunctionProps{
		Architecture: awslambda.Architecture_ARM_64(),
		Code: awslambda.Code_FromCustomCommand(jsii.String("cdk.out/ssr-lambda"), &[]*string{
			jsii.String("sh"),
			jsii.String("-c"),
			jsii.String("mkdir -p cdk.out/ssr-lambda && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o cdk.out/ssr-lambda/bootstrap ../cmd/ssr"),
		}, &awslambda.CustomCommandOptions{
			DeployTime:  jsii.Bool(true),
			DisplayName: jsii.String("ssr-lambda"),
		}),
		Description: jsii.String("Server-side HTML renderer for thailandgiftshop.com"),
		Environment: &map[string]*string{
			catalog.EnvTableName:                  catalogTable.TableName(),
			catalog.EnvSlugIndexName:              jsii.String(catalog.DefaultSlugIndexName),
			catalog.EnvPublicIndexName:            jsii.String(catalog.DefaultPublicIndexName),
			catalog.EnvRecentIndexName:            jsii.String(catalog.DefaultRecentIndexName),
			catalog.EnvProductImagePlaceholderURL: jsii.String(catalog.DefaultProductImagePlaceholderURL),
		},
		FunctionName: jsii.String("thailandgiftshop-ssr"),
		Handler:      jsii.String("bootstrap"),
		LogGroup:     lambdaLogGroup,
		MemorySize:   jsii.Number(128),
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(10)),
		Tracing:      awslambda.Tracing_ACTIVE,
	})
	catalogTable.GrantReadData(ssrFunction)

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

	awscdk.NewCfnOutput(stack, jsii.String("SsrHttpApiUrl"), &awscdk.CfnOutputProps{
		Description: jsii.String("Base URL for the SSR HTTP API"),
		Value:       httpAPI.ApiEndpoint(),
	})

	return httpAPI
}

func addSite(stack awscdk.Stack, httpAPI awsapigatewayv2.HttpApi) {
	hostedZone := siteHostedZone(stack)
	certificate := awscertificatemanager.NewCertificate(stack, jsii.String("SiteCertificate"), &awscertificatemanager.CertificateProps{
		DomainName: jsii.String(siteDomainName),
		SubjectAlternativeNames: &[]*string{
			jsii.String(wwwDomainName),
		},
		Validation: awscertificatemanager.CertificateValidation_FromDns(hostedZone),
	})
	securityHeadersPolicy := siteSecurityHeaders(stack)

	staticBucket := awss3.NewBucket(stack, jsii.String("StaticAssetsBucket"), &awss3.BucketProps{
		BlockPublicAccess: awss3.BlockPublicAccess_BLOCK_ALL(),
		Encryption:        awss3.BucketEncryption_S3_MANAGED,
		EnforceSSL:        jsii.Bool(true),
		ObjectOwnership:   awss3.ObjectOwnership_BUCKET_OWNER_ENFORCED,
		RemovalPolicy:     awscdk.RemovalPolicy_RETAIN,
	})
	productImagesBucket := awss3.NewBucket(stack, jsii.String("ProductImagesBucket"), &awss3.BucketProps{
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
			AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_GET_HEAD(),
			CachePolicy:           awscloudfront.CachePolicy_CACHING_DISABLED(),
			Compress:              jsii.Bool(true),
			Origin:                ssrOrigin(httpAPI),
			OriginRequestPolicy:   awscloudfront.OriginRequestPolicy_ALL_VIEWER_EXCEPT_HOST_HEADER(),
			ResponseHeadersPolicy: securityHeadersPolicy,
			ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
		},
		AdditionalBehaviors: &map[string]*awscloudfront.BehaviorOptions{
			"static/*": {
				AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_GET_HEAD(),
				CachePolicy:           awscloudfront.CachePolicy_CACHING_OPTIMIZED(),
				Compress:              jsii.Bool(true),
				Origin:                awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(staticBucket, nil),
				ResponseHeadersPolicy: securityHeadersPolicy,
				ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
			},
			"images/*": {
				AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_GET_HEAD(),
				CachePolicy:           awscloudfront.CachePolicy_CACHING_OPTIMIZED(),
				Compress:              jsii.Bool(true),
				Origin:                awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(productImagesBucket, nil),
				ResponseHeadersPolicy: securityHeadersPolicy,
				ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
			},
		},
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
				ContentSecurityPolicy: jsii.String("default-src 'self'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'; manifest-src 'self'"),
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

func ssrOrigin(httpAPI awsapigatewayv2.HttpApi) awscloudfront.IOrigin {
	return awscloudfrontorigins.NewHttpOrigin(apiGatewayDomainName(httpAPI), &awscloudfrontorigins.HttpOriginProps{
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
