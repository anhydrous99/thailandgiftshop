package main

import (
	"testing"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/assertions"
	"github.com/aws/jsii-runtime-go"
)

func TestStackSynthesizes(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)

	if stack == nil {
		t.Fatal("expected stack to be created")
	}
}

func TestStackIncludesObservabilityResources(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]interface{}{
		"LogGroupName":    "/aws/lambda/thailandgiftshop-ssr",
		"RetentionInDays": 30,
	})

	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]interface{}{
		"LogGroupName":    "/aws/apigateway/thailandgiftshop-ssr",
		"RetentionInDays": 30,
	})

	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]interface{}{
		"FunctionName": "thailandgiftshop-ssr",
		"LoggingConfig": map[string]interface{}{
			"LogGroup": map[string]interface{}{
				"Ref": "SsrLambdaLogGroup5806F2F2",
			},
		},
		"TracingConfig": map[string]interface{}{
			"Mode": "Active",
		},
	})

	template.HasResourceProperties(jsii.String("AWS::IAM::Policy"), map[string]interface{}{
		"PolicyDocument": map[string]interface{}{
			"Statement": assertions.Match_ArrayWith(&[]interface{}{
				assertions.Match_ObjectLike(&map[string]interface{}{
					"Action": assertions.Match_ArrayWith(&[]interface{}{
						"xray:PutTraceSegments",
						"xray:PutTelemetryRecords",
					}),
					"Effect":   "Allow",
					"Resource": "*",
				}),
			}),
		},
	})

	template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Stage"), map[string]interface{}{
		"AccessLogSettings": map[string]interface{}{
			"DestinationArn": assertions.Match_AnyValue(),
			"Format":         assertions.Match_StringLikeRegexp(jsii.String("requestId")),
		},
		"DefaultRouteSettings": map[string]interface{}{
			"DetailedMetricsEnabled": true,
		},
	})
}
