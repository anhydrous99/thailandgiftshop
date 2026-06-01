package main

import (
	"os"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2integrations"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
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

	addSSR(stack)

	return stack
}

func addSSR(stack awscdk.Stack) {
	lambdaLogGroup := awslogs.NewLogGroup(stack, jsii.String("SsrLambdaLogGroup"), &awslogs.LogGroupProps{
		LogGroupName: jsii.String("/aws/lambda/thailandgiftshop-ssr"),
		Retention:    awslogs.RetentionDays_ONE_MONTH,
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
		Description:  jsii.String("Server-side HTML renderer for thailandgiftshop.com"),
		FunctionName: jsii.String("thailandgiftshop-ssr"),
		Handler:      jsii.String("bootstrap"),
		LogGroup:     lambdaLogGroup,
		MemorySize:   jsii.Number(128),
		Runtime:      awslambda.Runtime_PROVIDED_AL2023(),
		Timeout:      awscdk.Duration_Seconds(jsii.Number(10)),
		Tracing:      awslambda.Tracing_ACTIVE,
	})

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
		Retention:    awslogs.RetentionDays_ONE_MONTH,
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
	region := os.Getenv("CDK_DEFAULT_REGION")

	if account == "" && region == "" {
		return nil
	}

	return &awscdk.Environment{
		Account: stringOrNil(account),
		Region:  stringOrNil(region),
	}
}

func stringOrNil(value string) *string {
	if value == "" {
		return nil
	}

	return jsii.String(value)
}
