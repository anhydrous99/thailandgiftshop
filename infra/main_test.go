package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	adminauth "github.com/anhydrous99/thailandgiftshop/internal/admin"
	appenv "github.com/anhydrous99/thailandgiftshop/internal/appenv"
	cartsession "github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/email"
	appobservability "github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
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

func TestStackRejectsConcreteNonProductionRegion(t *testing.T) {
	defer jsii.Close()

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewThailandGiftshopStack did not panic for concrete non-production region")
		}
	}()
	app := awscdk.NewApp(nil)
	NewThailandGiftshopStack(app, "TestStack", &ThailandGiftshopStackProps{
		StackProps: awscdk.StackProps{
			Env: &awscdk.Environment{
				Region: jsii.String("us-east-2"),
			},
		},
	})
}

func TestStackIncludesObservabilityResources(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.ResourceCountIs(jsii.String("AWS::Logs::LogGroup"), jsii.Number(3))
	template.AllResourcesProperties(jsii.String("AWS::Logs::LogGroup"), map[string]any{
		"RetentionInDays": 90,
	})

	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]any{
		"LogGroupName":    "/aws/lambda/thailandgiftshop-ssr",
		"RetentionInDays": 90,
	})

	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]any{
		"LogGroupName":    "/aws/apigateway/thailandgiftshop-ssr",
		"RetentionInDays": 90,
	})

	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]any{
		"LogGroupName":    "/aws/lambda/thailandgiftshop-static-assets-deployment",
		"RetentionInDays": 90,
	})
	template.HasResourceProperties(jsii.String("Custom::LogRetention"), map[string]any{
		"LogGroupName":    adminLambdaLogGroupName,
		"RetentionInDays": 90,
	})

	templateJSON := template.ToJSON()
	if managedLogGroupExists(t, templateJSON, adminLambdaLogGroupName) {
		t.Fatalf("%s must be imported instead of managed as AWS::Logs::LogGroup to avoid CloudFormation collisions with existing Lambda log groups", adminLambdaLogGroupName)
	}

	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]any{
		"FunctionName":  "thailandgiftshop-ssr",
		"MemorySize":    512,
		"Architectures": []any{"arm64"},
		"Runtime":       "provided.al2023",
		"LoggingConfig": map[string]any{
			"LogGroup": map[string]any{
				"Ref": "SsrLambdaLogGroup5806F2F2",
			},
		},
		"TracingConfig": map[string]any{
			"Mode": "Active",
		},
	})

	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]any{
		"Handler": "index.handler",
		"LoggingConfig": map[string]any{
			"LogGroup": assertions.Match_ObjectLike(&map[string]any{
				"Ref": assertions.Match_StringLikeRegexp(jsii.String("StaticAssetsDeploymentLogGroup")),
			}),
		},
	})

	template.HasResourceProperties(jsii.String("AWS::IAM::Policy"), map[string]any{
		"PolicyDocument": map[string]any{
			"Statement": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"Action": assertions.Match_ArrayWith(&[]any{
						"xray:PutTraceSegments",
						"xray:PutTelemetryRecords",
					}),
					"Effect":   "Allow",
					"Resource": "*",
				}),
			}),
		},
	})

	template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Stage"), map[string]any{
		"AccessLogSettings": map[string]any{
			"DestinationArn": assertions.Match_AnyValue(),
			"Format":         assertions.Match_StringLikeRegexp(jsii.String("requestId")),
		},
		"DefaultRouteSettings": map[string]any{
			"DetailedMetricsEnabled": true,
		},
	})

	template.ResourceCountIs(jsii.String("AWS::CloudWatch::Dashboard"), jsii.Number(1))
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Dashboard"), map[string]any{
		"DashboardName": operationsDashboardName,
	})
	template.HasParameter(jsii.String("AlarmNotificationEmail"), map[string]any{
		"Type":        "String",
		"Description": "Email address subscribed to critical operations alarms",
	})
	template.ResourceCountIs(jsii.String("AWS::SNS::Topic"), jsii.Number(1))
	template.HasResourceProperties(jsii.String("AWS::SNS::Topic"), map[string]any{
		"TopicName": operationsAlarmTopicName,
	})
	template.ResourceCountIs(jsii.String("AWS::SNS::Subscription"), jsii.Number(1))
	template.HasResourceProperties(jsii.String("AWS::SNS::Subscription"), map[string]any{
		"Protocol": "email",
		"Endpoint": map[string]any{
			"Ref": "AlarmNotificationEmail",
		},
		"TopicArn": assertions.Match_AnyValue(),
	})
	template.ResourceCountIs(jsii.String("AWS::CloudWatch::Alarm"), jsii.Number(20))
	template.AllResourcesProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"TreatMissingData": "notBreaching",
	})

	criticalAlarmNames := map[string]bool{
		"ThailandGiftshop-CloudFront-5xxRate-High":    true,
		"ThailandGiftshop-HttpApi-5xx-High":           true,
		"ThailandGiftshop-SsrLambda-Errors":           true,
		"ThailandGiftshop-AdminLambda-Errors":         true,
		"ThailandGiftshop-Admin-OriginRejected":       true,
		"ThailandGiftshop-CatalogWrite-Errors":        true,
		"ThailandGiftshop-ProductImageUpload-Errors":  true,
		"ThailandGiftshop-StripeWebhook-Errors":       true,
		"ThailandGiftshop-CheckoutPayment-Errors":     true,
		"ThailandGiftshop-CheckoutRefund-Errors":      true,
		"ThailandGiftshop-StockAdjust-RollbackErrors": true,
	}
	assertCriticalAlarmActions(t, templateJSON, criticalAlarmNames)

	appAlarmMetrics := map[string]struct {
		metricName string
		service    string
		outcomes   []string
	}{
		"ThailandGiftshop-Admin-OriginRejected":       {appobservability.MetricAdminOriginRejected, "admin", []string{"rejected"}},
		"ThailandGiftshop-CatalogWrite-Errors":        {appobservability.MetricCatalogWrite, "admin", []string{"error"}},
		"ThailandGiftshop-ProductImageUpload-Errors":  {appobservability.MetricProductImageUpload, "admin", []string{"error"}},
		"ThailandGiftshop-StripeWebhook-Errors":       {appobservability.MetricStripeWebhook, "ssr", []string{"invalid_signature", "amount_mismatch", "refund_mismatch", "paid_after_terminal", "error"}},
		"ThailandGiftshop-CheckoutPayment-Errors":     {appobservability.MetricCheckoutPayment, "checkout", []string{"provider_error", "error"}},
		"ThailandGiftshop-CheckoutRefund-Errors":      {appobservability.MetricCheckoutRefund, "checkout", []string{"failed", "provider_error", "error"}},
		"ThailandGiftshop-StockAdjust-RollbackErrors": {appobservability.MetricStockAdjust, "catalog", []string{"rollback_error"}},
	}
	for alarmName, expected := range appAlarmMetrics {
		template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
			"AlarmName": alarmName,
		})
		alarmText := templateValueString(t, cloudWatchAlarmProperties(t, templateJSON, alarmName))
		wants := append([]string{appobservability.Namespace, expected.metricName, "Service", expected.service}, expected.outcomes...)
		for _, want := range wants {
			if !strings.Contains(alarmText, want) {
				t.Fatalf("alarm %s missing %q: %s", alarmName, want, alarmText)
			}
		}
	}

	templateText := templateValueString(t, templateJSON)
	for _, want := range []string{
		"Thailand Gift Shop operations",
		"AWS/ApiGateway",
		"AWS/CloudFront",
		"AWS/DynamoDB",
		"AWS/Lambda",
		"AWS/S3",
		"AWS/WAFV2",
		appobservability.Namespace,
		appobservability.MetricAdminLoginAttempt,
		appobservability.MetricCatalogWrite,
		appobservability.MetricProductImageUpload,
		appobservability.MetricCustomerAuth,
		appobservability.MetricCheckoutPayment,
		appobservability.MetricStripeWebhook,
		appobservability.MetricOrderTransition,
		appobservability.MetricStockAdjust,
		appobservability.MetricCommerceOperation,
		appobservability.MetricCommerceOperationMs,
		"Critical alarm actions publish to SNS; watchlist alarms remain dashboard-only.",
	} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("observability template missing %q", want)
		}
	}
}

func TestStackUsesOptimizedLambdaBuildCommands(t *testing.T) {
	defer jsii.Close()

	if got, want := ssrLambdaBuildCommand, "mkdir -p cdk.out/ssr-lambda && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags lambda.norpc -ldflags \"-s -w\" -o cdk.out/ssr-lambda/bootstrap ../cmd/ssr"; got != want {
		t.Fatalf("ssr build command = %q, want %q", got, want)
	}
	if got, want := adminLambdaBuildCommand, "mkdir -p cdk.out/admin-lambda && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags lambda.norpc -ldflags \"-s -w\" -o cdk.out/admin-lambda/bootstrap ../cmd/admin"; got != want {
		t.Fatalf("admin build command = %q, want %q", got, want)
	}
}

func TestStackIncludesSsrCartCookieSecret(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResourceProperties(jsii.String("AWS::SecretsManager::Secret"), map[string]any{
		"Description": "Signing secret for thailandgiftshop.com cart cookies",
		"GenerateSecretString": map[string]any{
			"ExcludePunctuation": true,
			"PasswordLength":     64,
		},
	})
	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]any{
		"FunctionName":  "thailandgiftshop-ssr",
		"MemorySize":    512,
		"Architectures": []any{"arm64"},
		"Runtime":       "provided.al2023",
		"Environment": map[string]any{
			"Variables": assertions.Match_ObjectLike(&map[string]any{
				appenv.EnvAppEnvironment:    appenv.EnvironmentProduction,
				cartsession.EnvCookieSecret: assertions.Match_AnyValue(),
			}),
		},
	})
}

func TestStackIncludesAdminLambdaRoutesAndScopedPermissions(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.ResourceCountIs(jsii.String("AWS::ApiGatewayV2::Route"), jsii.Number(3))
	template.ResourceCountIs(jsii.String("AWS::ApiGatewayV2::Integration"), jsii.Number(2))

	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]any{
		"FunctionName":  "thailandgiftshop-admin",
		"MemorySize":    512,
		"Architectures": []any{"arm64"},
		"Environment": map[string]any{
			"Variables": map[string]any{
				"CATALOG_TABLE_NAME":                     assertions.Match_AnyValue(),
				"CATALOG_SLUG_INDEX_NAME":                catalog.DefaultSlugIndexName,
				"CATALOG_PUBLIC_INDEX_NAME":              catalog.DefaultPublicIndexName,
				"CATALOG_RECENT_INDEX_NAME":              catalog.DefaultRecentIndexName,
				"CATALOG_ENTITY_INDEX_NAME":              catalog.DefaultEntityIndexName,
				appenv.EnvAppEnvironment:                 appenv.EnvironmentProduction,
				adminauth.EnvProductImagesBucketName:     assertions.Match_AnyValue(),
				adminauth.EnvProductImagesKeyPrefix:      productImagesKeyPrefix,
				adminauth.EnvAdminCredentialsSecretName:  assertions.Match_AnyValue(),
				adminauth.EnvAdminLoginAttemptsTableName: assertions.Match_AnyValue(),
				adminauth.EnvAdminOriginHeaderSecret:     assertions.Match_AnyValue(),
			},
		},
		"Handler": "bootstrap",
		"LoggingConfig": map[string]any{
			"LogGroup": adminLambdaLogGroupName,
		},
		"Runtime": "provided.al2023",
		"TracingConfig": map[string]any{
			"Mode": "Active",
		},
	})
	template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Route"), map[string]any{
		"RouteKey": "ANY /admin",
	})
	template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Route"), map[string]any{
		"RouteKey": "ANY /admin/{proxy+}",
	})
	template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Integration"), map[string]any{
		"IntegrationType":      "AWS_PROXY",
		"PayloadFormatVersion": "2.0",
		"IntegrationUri": map[string]any{
			"Fn::GetAtt": assertions.Match_ArrayWith(&[]any{
				assertions.Match_StringLikeRegexp(jsii.String("AdminLambda")),
				"Arn",
			}),
		},
	})

	templateJSON := template.ToJSON()
	ssrActions := policyActionsForFunction(t, templateJSON, "thailandgiftshop-ssr")
	adminActions := policyActionsForFunction(t, templateJSON, "thailandgiftshop-admin")

	for _, action := range []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem", "dynamodb:TransactWriteItems", "s3:GetObject*", "s3:PutObject"} {
		if !containsAction(adminActions, action) {
			t.Fatalf("admin lambda policy actions missing %s; got %v", action, adminActions)
		}
	}
	for _, action := range []string{"s3:PutObject", "s3:DeleteObject*"} {
		if containsAction(ssrActions, action) {
			t.Fatalf("public SSR lambda must not include %s; got %v", action, ssrActions)
		}
	}

	if !policyForFunctionHasActionOnCatalogTable(t, templateJSON, "thailandgiftshop-admin", "dynamodb:TransactWriteItems") {
		t.Fatalf("admin lambda policy must allow dynamodb:TransactWriteItems on the catalog table; got %v", adminActions)
	}
	// Checkout reserves and releases stock through the catalog store's
	// versioned UpdateProduct transaction, so the public SSR lambda writes to
	// the catalog and commerce tables — and nothing else.
	for _, action := range []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:TransactWriteItems"} {
		if !policyForFunctionHasActionOnCatalogTable(t, templateJSON, "thailandgiftshop-ssr", action) {
			t.Fatalf("public SSR lambda must allow %s on the catalog table for stock adjustments; got %v", action, ssrActions)
		}
		if !policyForFunctionHasActionOnCommerceTable(t, templateJSON, "thailandgiftshop-ssr", action) {
			t.Fatalf("public SSR lambda must allow %s on the commerce table; got %v", action, ssrActions)
		}
		if policyForFunctionHasActionOnAdminLoginAttemptsTable(t, templateJSON, "thailandgiftshop-ssr", action) {
			t.Fatalf("public SSR lambda must not allow %s on the admin login attempts table; got %v", action, ssrActions)
		}
	}
}

func TestStackIncludesAdminLoginAttemptThrottleStorage(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResource(jsii.String("AWS::DynamoDB::Table"), map[string]any{
		"DeletionPolicy":      "Delete",
		"UpdateReplacePolicy": "Delete",
		"Properties": assertions.Match_ObjectLike(&map[string]any{
			"TableName":   assertions.Match_Absent(),
			"BillingMode": "PAY_PER_REQUEST",
			"KeySchema": assertions.Match_ArrayWith(&[]any{
				map[string]any{
					"AttributeName": adminLoginAttemptsPKName,
					"KeyType":       "HASH",
				},
			}),
			"PointInTimeRecoverySpecification": map[string]any{
				"PointInTimeRecoveryEnabled": true,
			},
			"TimeToLiveSpecification": map[string]any{
				"AttributeName": adminLoginAttemptsTTLName,
				"Enabled":       true,
			},
		}),
	})

	templateJSON := template.ToJSON()
	adminVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-admin")
	if _, found := adminVariables[adminauth.EnvAdminLoginAttemptsTableName]; !found {
		t.Fatalf("admin lambda missing %s env var: %#v", adminauth.EnvAdminLoginAttemptsTableName, adminVariables)
	}
	for _, action := range []string{"dynamodb:GetItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem"} {
		if !policyForFunctionHasActionOnAdminLoginAttemptsTable(t, templateJSON, "thailandgiftshop-admin", action) {
			t.Fatalf("admin lambda policy missing %s on admin login attempts table", action)
		}
	}
}

func TestStackIncludesAdminCloudFrontWAFRateLimits(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.ResourceCountIs(jsii.String("AWS::WAFv2::WebACL"), jsii.Number(1))
	template.HasResourceProperties(jsii.String("AWS::WAFv2::WebACL"), map[string]any{
		"Scope": "CLOUDFRONT",
		"DefaultAction": map[string]any{
			"Allow": map[string]any{},
		},
		"Rules": assertions.Match_ArrayWith(&[]any{
			assertions.Match_ObjectLike(&map[string]any{
				"Name": "AdminLoginPostRateLimit",
				"Action": map[string]any{
					"Block": map[string]any{
						"CustomResponse": map[string]any{
							"ResponseCode": 429,
						},
					},
				},
				"Statement": map[string]any{
					"RateBasedStatement": assertions.Match_ObjectLike(&map[string]any{
						"AggregateKeyType":    "IP",
						"Limit":               100,
						"EvaluationWindowSec": 300,
						"ScopeDownStatement": map[string]any{
							"AndStatement": map[string]any{
								"Statements": assertions.Match_ArrayWith(&[]any{
									urlDecodedPathMatchAssertion("/admin/login", "EXACTLY"),
									byteMatchAssertion("Method", "POST", "EXACTLY"),
								}),
							},
						},
					}),
				},
			}),
			assertions.Match_ObjectLike(&map[string]any{
				"Name": "AdminPathRateLimit",
				"Statement": map[string]any{
					"RateBasedStatement": assertions.Match_ObjectLike(&map[string]any{
						"AggregateKeyType":    "IP",
						"Limit":               500,
						"EvaluationWindowSec": 300,
						"ScopeDownStatement": map[string]any{
							"OrStatement": map[string]any{
								"Statements": assertions.Match_ArrayWith(&[]any{
									byteMatchAssertion("UriPath", "/admin", "EXACTLY"),
									byteMatchAssertion("UriPath", "/admin/", "STARTS_WITH"),
								}),
							},
						},
					}),
				},
			}),
			assertions.Match_ObjectLike(&map[string]any{
				"Name": "CustomerAuthPostRateLimit",
				"Action": map[string]any{
					"Block": map[string]any{
						"CustomResponse": map[string]any{
							"ResponseCode": 429,
						},
					},
				},
				"Statement": map[string]any{
					"RateBasedStatement": assertions.Match_ObjectLike(&map[string]any{
						"AggregateKeyType":    "IP",
						"Limit":               100,
						"EvaluationWindowSec": 300,
						"ScopeDownStatement": map[string]any{
							"AndStatement": map[string]any{
								"Statements": assertions.Match_ArrayWith(&[]any{
									assertions.Match_ObjectLike(&map[string]any{
										"OrStatement": map[string]any{
											"Statements": assertions.Match_ArrayWith(&[]any{
												urlDecodedPathMatchAssertion("/account/sign-in", "EXACTLY"),
												urlDecodedPathMatchAssertion("/account/sign-up", "EXACTLY"),
											}),
										},
									}),
									byteMatchAssertion("Method", "POST", "EXACTLY"),
								}),
							},
						},
					}),
				},
			}),
			assertions.Match_ObjectLike(&map[string]any{
				"Name":     "CheckoutPlaceOrderRateLimit",
				"Priority": 3,
				"Action": map[string]any{
					"Block": map[string]any{
						"CustomResponse": map[string]any{
							"ResponseCode": 429,
						},
					},
				},
				"Statement": map[string]any{
					"RateBasedStatement": assertions.Match_ObjectLike(&map[string]any{
						"AggregateKeyType":    "IP",
						"Limit":               100,
						"EvaluationWindowSec": 300,
						"ScopeDownStatement": map[string]any{
							"AndStatement": map[string]any{
								"Statements": assertions.Match_ArrayWith(&[]any{
									urlDecodedPathMatchAssertion("/checkout/place-order", "EXACTLY"),
									byteMatchAssertion("Method", "POST", "EXACTLY"),
								}),
							},
						},
					}),
				},
			}),
		}),
	})
	template.HasResourceProperties(jsii.String("AWS::CloudFront::Distribution"), map[string]any{
		"DistributionConfig": assertions.Match_ObjectLike(&map[string]any{
			"WebACLId": map[string]any{
				"Fn::GetAtt": []any{"AdminCloudFrontWebACL", "Arn"},
			},
		}),
	})
}

func TestStackWiresWafObservability(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	// The Rule=ALL blocked-requests alarm keeps its pre-customer-auth ID and
	// name (renaming would replace the alarm resource), but its description
	// must say it covers every edge rule, not just admin traffic.
	template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"AlarmName":        "ThailandGiftshop-WAF-AdminBlocks",
		"AlarmDescription": "WAF blocked requests across all edge rules (admin login, admin path, and customer auth) exceeded the normal operating threshold.",
		"Threshold":        10,
	})

	// Each per-rule block metric needs dashboard visibility so an operator
	// can attribute a Rule=ALL alarm to admin login, or customer auth bursts.
	templateJSON := template.ToJSON()
	dashboardBody := operationsDashboardBodyJSON(t, templateJSON)
	for _, fragment := range []string{
		"ThailandGiftshopAdminCloudFrontLoginPost",
		"ThailandGiftshopAdminCloudFrontCustomerAuthPost",
		"WAF customer auth blocks",
	} {
		if !strings.Contains(dashboardBody, fragment) {
			t.Fatalf("operations dashboard body missing %q", fragment)
		}
	}
}

func operationsDashboardBodyJSON(t *testing.T, templateJSON *map[string]any) string {
	t.Helper()

	for _, resource := range templateResources(t, templateJSON) {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::CloudWatch::Dashboard" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		encoded, err := json.Marshal(properties["DashboardBody"])
		if err != nil {
			t.Fatalf("marshal dashboard body: %v", err)
		}
		return string(encoded)
	}

	t.Fatal("operations dashboard not found")
	return ""
}

func TestStackWiresAdminCredentialsSecretName(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)
	template.ResourceCountIs(jsii.String("AWS::SecretsManager::Secret"), jsii.Number(3))

	templateJSON := template.ToJSON()

	// The admin lambda receives the secret name and reads it at runtime, rather
	// than a deploy-time dynamic reference baked into an env var (which would not
	// pick up a rotation without a redeploy).
	adminVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-admin")
	secretName := templateValueString(t, adminVariables[adminauth.EnvAdminCredentialsSecretName])
	if secretName != `"`+adminCredentialsSecretName+`"` {
		t.Fatalf("admin credentials secret name = %q, want %q", secretName, adminCredentialsSecretName)
	}
	if _, found := adminVariables[adminauth.EnvAdminCredentialsSecretJSON]; found {
		t.Fatalf("admin lambda must not receive deploy-time %s dynamic reference: %#v", adminauth.EnvAdminCredentialsSecretJSON, adminVariables)
	}

	ssrVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-ssr")
	for _, key := range []string{adminauth.EnvAdminCredentialsSecretName, adminauth.EnvAdminCredentialsSecretJSON} {
		if _, found := ssrVariables[key]; found {
			t.Fatalf("public SSR lambda must not receive %s; got %#v", key, ssrVariables)
		}
	}

	templateText := templateValueString(t, templateJSON)
	for _, want := range []string{"secretsmanager:GetSecretValue", adminCredentialsSecretName} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("template missing %q for runtime admin secret lookup", want)
		}
	}
}

func TestStackWiresAdminOriginHeaderSecretThroughCloudFrontAndAdminLambda(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResourceProperties(jsii.String("AWS::SecretsManager::Secret"), map[string]any{
		"Description": "Shared origin header secret for thailandgiftshop.com admin requests through CloudFront",
		"GenerateSecretString": map[string]any{
			"ExcludePunctuation": true,
			"PasswordLength":     64,
		},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudFront::Distribution"), map[string]any{
		"DistributionConfig": assertions.Match_ObjectLike(&map[string]any{
			"Origins": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"OriginCustomHeaders": assertions.Match_ArrayWith(&[]any{
						assertions.Match_ObjectLike(&map[string]any{
							"HeaderName": "X-TGS-Origin-Secret",
						}),
					}),
				}),
			}),
		}),
	})

	templateJSON := template.ToJSON()
	adminVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-admin")
	adminOriginSecret, found := adminVariables[adminauth.EnvAdminOriginHeaderSecret]
	if !found {
		t.Fatalf("admin lambda missing %s env var: %#v", adminauth.EnvAdminOriginHeaderSecret, adminVariables)
	}
	originHeaderValue := cloudFrontAPIOriginCustomHeaderValue(t, templateJSON, "X-TGS-Origin-Secret")
	if templateValueString(t, originHeaderValue) != templateValueString(t, adminOriginSecret) {
		t.Fatalf("CloudFront origin header and admin lambda env use different secrets:\norigin=%s\nenv=%s", templateValueString(t, originHeaderValue), templateValueString(t, adminOriginSecret))
	}
	if value := templateValueString(t, adminOriginSecret); !strings.Contains(value, "AdminOriginHeaderSecret") || !strings.Contains(value, "SecretString") {
		t.Fatalf("admin origin secret reference = %s, want generated AdminOriginHeaderSecret SecretString", value)
	}

	ssrVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-ssr")
	ssrOriginSecret, found := ssrVariables[adminauth.EnvAdminOriginHeaderSecret]
	if !found {
		t.Fatalf("ssr lambda missing %s env var: %#v", adminauth.EnvAdminOriginHeaderSecret, ssrVariables)
	}
	if templateValueString(t, originHeaderValue) != templateValueString(t, ssrOriginSecret) {
		t.Fatalf("CloudFront origin header and ssr lambda env use different secrets:\norigin=%s\nenv=%s", templateValueString(t, originHeaderValue), templateValueString(t, ssrOriginSecret))
	}
}

func TestStackIncludesCatalogResources(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResource(jsii.String("AWS::DynamoDB::Table"), map[string]any{
		"DeletionPolicy":      "Retain",
		"UpdateReplacePolicy": "Retain",
		"Properties": assertions.Match_ObjectLike(&map[string]any{
			"AttributeDefinitions": assertions.Match_ArrayWith(&[]any{
				map[string]any{
					"AttributeName": catalogPartitionKeyName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogSortKeyName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogSlugIndexPKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogSlugIndexSKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogPublicIndexPKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogPublicIndexSKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogRecentIndexPKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogRecentIndexSKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogEntityIndexPKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": catalogEntityIndexSKName,
					"AttributeType": "S",
				},
			}),
			"BillingMode": "PAY_PER_REQUEST",
			"GlobalSecondaryIndexes": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"IndexName": "slug-index",
					"KeySchema": assertions.Match_ArrayWith(&[]any{
						map[string]any{
							"AttributeName": catalogSlugIndexPKName,
							"KeyType":       "HASH",
						},
						map[string]any{
							"AttributeName": catalogSlugIndexSKName,
							"KeyType":       "RANGE",
						},
					}),
					"Projection": map[string]any{
						"ProjectionType": "ALL",
					},
				}),
				assertions.Match_ObjectLike(&map[string]any{
					"IndexName": "public-index",
					"KeySchema": assertions.Match_ArrayWith(&[]any{
						map[string]any{
							"AttributeName": catalogPublicIndexPKName,
							"KeyType":       "HASH",
						},
						map[string]any{
							"AttributeName": catalogPublicIndexSKName,
							"KeyType":       "RANGE",
						},
					}),
					"Projection": map[string]any{
						"ProjectionType": "ALL",
					},
				}),
				assertions.Match_ObjectLike(&map[string]any{
					"IndexName": "recent-index",
					"KeySchema": assertions.Match_ArrayWith(&[]any{
						map[string]any{
							"AttributeName": catalogRecentIndexPKName,
							"KeyType":       "HASH",
						},
						map[string]any{
							"AttributeName": catalogRecentIndexSKName,
							"KeyType":       "RANGE",
						},
					}),
					"Projection": map[string]any{
						"ProjectionType": "ALL",
					},
				}),
				assertions.Match_ObjectLike(&map[string]any{
					"IndexName": "entity-index",
					"KeySchema": assertions.Match_ArrayWith(&[]any{
						map[string]any{
							"AttributeName": catalogEntityIndexPKName,
							"KeyType":       "HASH",
						},
						map[string]any{
							"AttributeName": catalogEntityIndexSKName,
							"KeyType":       "RANGE",
						},
					}),
					"Projection": map[string]any{
						"ProjectionType": "ALL",
					},
				}),
			}),
			"KeySchema": assertions.Match_ArrayWith(&[]any{
				map[string]any{
					"AttributeName": catalogPartitionKeyName,
					"KeyType":       "HASH",
				},
				map[string]any{
					"AttributeName": catalogSortKeyName,
					"KeyType":       "RANGE",
				},
			}),
			"PointInTimeRecoverySpecification": map[string]any{
				"PointInTimeRecoveryEnabled": true,
			},
			"SSESpecification": map[string]any{
				"SSEEnabled": true,
			},
			"TableName": catalogTableName,
		}),
	})

	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]any{
		"FunctionName": "thailandgiftshop-ssr",
		"Environment": map[string]any{
			"Variables": map[string]any{
				"CATALOG_TABLE_NAME":            assertions.Match_AnyValue(),
				"CATALOG_SLUG_INDEX_NAME":       "slug-index",
				"CATALOG_PUBLIC_INDEX_NAME":     "public-index",
				"CATALOG_RECENT_INDEX_NAME":     "recent-index",
				"CATALOG_ENTITY_INDEX_NAME":     "entity-index",
				"PRODUCT_IMAGE_PLACEHOLDER_URL": catalog.DefaultProductImagePlaceholderURL,
				cartsession.EnvCookieSecret:     assertions.Match_AnyValue(),
			},
		},
	})

	template.HasResourceProperties(jsii.String("AWS::IAM::Policy"), map[string]any{
		"PolicyDocument": map[string]any{
			"Statement": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"Action": assertions.Match_ArrayWith(&[]any{
						"dynamodb:BatchGetItem",
						"dynamodb:Query",
						"dynamodb:GetItem",
					}),
					"Effect":   "Allow",
					"Resource": assertions.Match_AnyValue(),
				}),
			}),
		},
	})

	template.HasOutput(jsii.String("CatalogTableName"), map[string]any{
		"Description": "DynamoDB table name for products and categories",
		"Value":       assertions.Match_AnyValue(),
	})
	template.HasOutput(jsii.String("CatalogTableArn"), map[string]any{
		"Description": "DynamoDB table ARN for products and categories",
		"Value":       assertions.Match_AnyValue(),
	})
}

func TestStackIncludesCommerceTable(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResource(jsii.String("AWS::DynamoDB::Table"), map[string]any{
		"DeletionPolicy":      "Retain",
		"UpdateReplacePolicy": "Retain",
		"Properties": assertions.Match_ObjectLike(&map[string]any{
			"TableName": commerceTableName,
			"AttributeDefinitions": assertions.Match_ArrayWith(&[]any{
				map[string]any{
					"AttributeName": commercePartitionKeyName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": commerceSortKeyName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": commerceCustomerOrdersIndexPKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": commerceCustomerOrdersIndexSKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": commerceOrdersIndexPKName,
					"AttributeType": "S",
				},
				map[string]any{
					"AttributeName": commerceOrdersIndexSKName,
					"AttributeType": "S",
				},
			}),
			"BillingMode": "PAY_PER_REQUEST",
			"GlobalSecondaryIndexes": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"IndexName": commerce.DefaultCustomerOrdersIndexName,
					"KeySchema": assertions.Match_ArrayWith(&[]any{
						map[string]any{
							"AttributeName": commerceCustomerOrdersIndexPKName,
							"KeyType":       "HASH",
						},
						map[string]any{
							"AttributeName": commerceCustomerOrdersIndexSKName,
							"KeyType":       "RANGE",
						},
					}),
					"Projection": map[string]any{
						"ProjectionType": "ALL",
					},
				}),
				assertions.Match_ObjectLike(&map[string]any{
					"IndexName": commerce.DefaultOrdersIndexName,
					"KeySchema": assertions.Match_ArrayWith(&[]any{
						map[string]any{
							"AttributeName": commerceOrdersIndexPKName,
							"KeyType":       "HASH",
						},
						map[string]any{
							"AttributeName": commerceOrdersIndexSKName,
							"KeyType":       "RANGE",
						},
					}),
					"Projection": map[string]any{
						"ProjectionType": "ALL",
					},
				}),
			}),
			"KeySchema": assertions.Match_ArrayWith(&[]any{
				map[string]any{
					"AttributeName": commercePartitionKeyName,
					"KeyType":       "HASH",
				},
				map[string]any{
					"AttributeName": commerceSortKeyName,
					"KeyType":       "RANGE",
				},
			}),
			"PointInTimeRecoverySpecification": map[string]any{
				"PointInTimeRecoveryEnabled": true,
			},
			"SSESpecification": map[string]any{
				"SSEEnabled": true,
			},
			"TimeToLiveSpecification": map[string]any{
				"AttributeName": commerceTTLAttributeName,
				"Enabled":       true,
			},
		}),
	})

	templateJSON := template.ToJSON()
	ssrVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-ssr")
	adminVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-admin")
	for name, variables := range map[string]map[string]any{
		"thailandgiftshop-ssr":   ssrVariables,
		"thailandgiftshop-admin": adminVariables,
	} {
		if _, found := variables[commerce.EnvTableName]; !found {
			t.Fatalf("%s lambda missing %s env var: %#v", name, commerce.EnvTableName, variables)
		}
		if got := variables[commerce.EnvCustomerOrdersIndexName]; got != commerce.DefaultCustomerOrdersIndexName {
			t.Fatalf("%s lambda %s = %v, want %q", name, commerce.EnvCustomerOrdersIndexName, got, commerce.DefaultCustomerOrdersIndexName)
		}
		if got := variables[commerce.EnvOrdersIndexName]; got != commerce.DefaultOrdersIndexName {
			t.Fatalf("%s lambda %s = %v, want %q", name, commerce.EnvOrdersIndexName, got, commerce.DefaultOrdersIndexName)
		}
	}
	if got := ssrVariables[payments.EnvPublicBaseURL]; got != payments.DefaultPublicBaseURL {
		t.Fatalf("ssr lambda %s = %v, want %q", payments.EnvPublicBaseURL, got, payments.DefaultPublicBaseURL)
	}
	if _, found := adminVariables[payments.EnvPublicBaseURL]; found {
		t.Fatalf("admin lambda must not receive %s; got %#v", payments.EnvPublicBaseURL, adminVariables)
	}

	for _, functionName := range []string{"thailandgiftshop-ssr", "thailandgiftshop-admin"} {
		for _, action := range []string{"dynamodb:GetItem", "dynamodb:Query", "dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:BatchWriteItem", "dynamodb:TransactWriteItems"} {
			if !policyForFunctionHasActionOnCommerceTable(t, templateJSON, functionName, action) {
				t.Fatalf("%s lambda policy missing %s on the commerce table", functionName, action)
			}
		}
	}

	template.HasOutput(jsii.String("CommerceTableName"), map[string]any{
		"Description": "DynamoDB table name for customers, carts, addresses, and orders",
		"Value":       assertions.Match_AnyValue(),
	})
	template.HasOutput(jsii.String("CommerceTableArn"), map[string]any{
		"Description": "DynamoDB table ARN for customers, carts, addresses, and orders",
		"Value":       assertions.Match_AnyValue(),
	})
	template.HasOutput(jsii.String("CommerceCustomerOrdersIndexName"), map[string]any{
		"Value": commerce.DefaultCustomerOrdersIndexName,
	})
	template.HasOutput(jsii.String("CommerceOrdersIndexName"), map[string]any{
		"Value": commerce.DefaultOrdersIndexName,
	})
}

func TestStackIncludesCustomerSessionSecret(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResourceProperties(jsii.String("AWS::SecretsManager::Secret"), map[string]any{
		"Description": "Signing secret for thailandgiftshop.com customer session cookies",
		"GenerateSecretString": map[string]any{
			"ExcludePunctuation": true,
			"PasswordLength":     64,
		},
	})

	templateJSON := template.ToJSON()
	ssrVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-ssr")
	sessionSecret, found := ssrVariables[commerce.EnvSessionSecret]
	if !found {
		t.Fatalf("ssr lambda missing %s env var: %#v", commerce.EnvSessionSecret, ssrVariables)
	}
	if value := templateValueString(t, sessionSecret); !strings.Contains(value, "CustomerSessionSecret") || !strings.Contains(value, "SecretString") {
		t.Fatalf("customer session secret reference = %s, want generated CustomerSessionSecret SecretString", value)
	}

	adminVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-admin")
	if _, found := adminVariables[commerce.EnvSessionSecret]; found {
		t.Fatalf("admin lambda must not receive %s; got %#v", commerce.EnvSessionSecret, adminVariables)
	}
}

func TestStackWiresStripeCredentialsSecretName(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	templateJSON := template.ToJSON()
	ssrVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-ssr")
	secretName := templateValueString(t, ssrVariables[payments.EnvStripeCredentialsSecretName])
	if secretName != `"`+stripeCredentialsSecretName+`"` {
		t.Fatalf("stripe credentials secret name = %q, want %q", secretName, stripeCredentialsSecretName)
	}
	if _, found := ssrVariables[payments.EnvStripeCredentialsSecretJSON]; found {
		t.Fatalf("ssr lambda must not receive deploy-time %s dynamic reference: %#v", payments.EnvStripeCredentialsSecretJSON, ssrVariables)
	}

	// The admin lambda needs the same credentials: canceling a pending order
	// verifies and expires its Stripe checkout session first, and without the
	// provider that session-expiry guard is silently skipped.
	adminVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-admin")
	adminSecretName := templateValueString(t, adminVariables[payments.EnvStripeCredentialsSecretName])
	if adminSecretName != `"`+stripeCredentialsSecretName+`"` {
		t.Fatalf("admin stripe credentials secret name = %q, want %q", adminSecretName, stripeCredentialsSecretName)
	}
	if _, found := adminVariables[payments.EnvStripeCredentialsSecretJSON]; found {
		t.Fatalf("admin lambda must not receive deploy-time %s dynamic reference: %#v", payments.EnvStripeCredentialsSecretJSON, adminVariables)
	}

	templateText := templateValueString(t, templateJSON)
	for _, want := range []string{"secretsmanager:GetSecretValue", stripeCredentialsSecretName} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("template missing %q for runtime Stripe secret lookup", want)
		}
	}
}

func TestStackIncludesSESDomainIdentityAndEmailPermissions(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResourceProperties(jsii.String("AWS::SES::EmailIdentity"), map[string]any{
		"EmailIdentity": siteDomainName,
		"DkimSigningAttributes": assertions.Match_ObjectLike(&map[string]any{
			"NextSigningKeyLength": "RSA_2048_BIT",
		}),
	})
	template.HasResourceProperties(jsii.String("AWS::Route53::RecordSet"), map[string]any{
		"HostedZoneId": map[string]any{"Ref": "HostedZoneId"},
	})

	templateJSON := template.ToJSON()
	ssrVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-ssr")
	adminVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-admin")
	for name, variables := range map[string]map[string]any{
		"thailandgiftshop-ssr":   ssrVariables,
		"thailandgiftshop-admin": adminVariables,
	} {
		if got := variables[email.EnvSenderMode]; got != email.SenderKindSES {
			t.Fatalf("%s %s = %v, want %q", name, email.EnvSenderMode, got, email.SenderKindSES)
		}
		if got := variables[email.EnvFromAddress]; got != email.DefaultFromAddress {
			t.Fatalf("%s %s = %v, want %q", name, email.EnvFromAddress, got, email.DefaultFromAddress)
		}
		if got := variables[email.EnvSESRegion]; got != productionRegion {
			t.Fatalf("%s %s = %v, want %q", name, email.EnvSESRegion, got, productionRegion)
		}
		if !policyForFunctionHasSESAction(t, templateJSON, name, "ses:SendEmail") {
			t.Fatalf("%s lambda policy missing ses:SendEmail on SES identity", name)
		}
	}
	if strings.Contains(templateValueString(t, templateJSON), "ses:*") {
		t.Fatal("template must not grant ses:* wildcard permissions")
	}
	if strings.Contains(templateValueString(t, templateJSON), "ses:SendRawEmail") {
		t.Fatal("template must not grant ses:SendRawEmail")
	}
}

func TestStackIncludesStaticAssetsDistribution(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasParameter(jsii.String("HostedZoneId"), map[string]any{
		"Type":        "String",
		"Description": "Route 53 public hosted zone ID for thailandgiftshop.com",
	})

	template.HasResourceProperties(jsii.String("AWS::S3::Bucket"), map[string]any{
		"BucketEncryption": map[string]any{
			"ServerSideEncryptionConfiguration": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"ServerSideEncryptionByDefault": map[string]any{
						"SSEAlgorithm": "AES256",
					},
				}),
			}),
		},
		"OwnershipControls": map[string]any{
			"Rules": assertions.Match_ArrayWith(&[]any{
				map[string]any{
					"ObjectOwnership": "BucketOwnerEnforced",
				},
			}),
		},
		"PublicAccessBlockConfiguration": map[string]any{
			"BlockPublicAcls":       true,
			"BlockPublicPolicy":     true,
			"IgnorePublicAcls":      true,
			"RestrictPublicBuckets": true,
		},
	})

	template.HasResourceProperties(jsii.String("AWS::S3::BucketPolicy"), map[string]any{
		"PolicyDocument": map[string]any{
			"Statement": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"Action": "s3:*",
					"Condition": map[string]any{
						"Bool": map[string]any{
							"aws:SecureTransport": "false",
						},
					},
					"Effect": "Deny",
				}),
				assertions.Match_ObjectLike(&map[string]any{
					"Action": "s3:GetObject",
					"Condition": map[string]any{
						"StringEquals": map[string]any{
							"AWS:SourceArn": assertions.Match_AnyValue(),
						},
					},
					"Effect": "Allow",
					"Principal": map[string]any{
						"Service": "cloudfront.amazonaws.com",
					},
				}),
			}),
		},
	})

	template.HasResourceProperties(jsii.String("AWS::CloudFront::OriginAccessControl"), map[string]any{
		"OriginAccessControlConfig": map[string]any{
			"OriginAccessControlOriginType": "s3",
			"SigningBehavior":               "always",
			"SigningProtocol":               "sigv4",
		},
	})

	template.HasResourceProperties(jsii.String("AWS::CertificateManager::Certificate"), map[string]any{
		"DomainName": siteDomainName,
		"SubjectAlternativeNames": assertions.Match_ArrayWith(&[]any{
			wwwDomainName,
		}),
		"ValidationMethod": "DNS",
	})

	template.HasResourceProperties(jsii.String("AWS::CloudFront::ResponseHeadersPolicy"), map[string]any{
		"ResponseHeadersPolicyConfig": map[string]any{
			"SecurityHeadersConfig": map[string]any{
				"ContentSecurityPolicy": map[string]any{
					"ContentSecurityPolicy": assertions.Match_StringLikeRegexp(jsii.String("default-src 'self'")),
					"Override":              true,
				},
				"ContentTypeOptions": map[string]any{
					"Override": true,
				},
				"FrameOptions": map[string]any{
					"FrameOption": "DENY",
					"Override":    true,
				},
				"ReferrerPolicy": map[string]any{
					"Override":       true,
					"ReferrerPolicy": assertions.Match_StringLikeRegexp(jsii.String("strict-origin")),
				},
				"StrictTransportSecurity": map[string]any{
					"AccessControlMaxAgeSec": 31536000,
					"Override":               true,
					"Preload":                false,
				},
			},
		},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudFront::ResponseHeadersPolicy"), map[string]any{
		"ResponseHeadersPolicyConfig": map[string]any{
			"SecurityHeadersConfig": map[string]any{
				"ContentSecurityPolicy": map[string]any{
					"ContentSecurityPolicy": assertions.Match_StringLikeRegexp(jsii.String("connect-src 'self'.*s3")),
					"Override":              true,
				},
			},
		},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudFront::ResponseHeadersPolicy"), map[string]any{
		"ResponseHeadersPolicyConfig": map[string]any{
			"SecurityHeadersConfig": map[string]any{
				"ContentSecurityPolicy": map[string]any{
					"ContentSecurityPolicy": assertions.Match_StringLikeRegexp(jsii.String("form-action 'self'.*s3")),
					"Override":              true,
				},
			},
		},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudFront::ResponseHeadersPolicy"), map[string]any{
		"ResponseHeadersPolicyConfig": map[string]any{
			"SecurityHeadersConfig": map[string]any{
				"ContentSecurityPolicy": map[string]any{
					"ContentSecurityPolicy": assertions.Match_StringLikeRegexp(jsii.String(`form-action 'self'[^;]*checkout\.stripe\.com`)),
					"Override":              true,
				},
			},
		},
	})
	template.HasResourceProperties(jsii.String("AWS::CloudFront::ResponseHeadersPolicy"), map[string]any{
		"ResponseHeadersPolicyConfig": map[string]any{
			"SecurityHeadersConfig": map[string]any{
				"ContentSecurityPolicy": map[string]any{
					"ContentSecurityPolicy": assertions.Match_StringLikeRegexp(jsii.String(`script-src 'self'; style-src 'self'`)),
					"Override":              true,
				},
			},
		},
	})

	template.ResourceCountIs(jsii.String("AWS::CloudFront::Function"), jsii.Number(1))
	template.HasResourceProperties(jsii.String("AWS::CloudFront::Function"), map[string]any{
		"AutoPublish":  true,
		"FunctionCode": assertions.Match_StringLikeRegexp(jsii.String("host !== \"www\\.thailandgiftshop\\.com\"[\\s\\S]*https://thailandgiftshop\\.com\" \\+ request\\.uri[\\s\\S]*queryParts\\.join[\\s\\S]*statusCode: 308")),
		"FunctionConfig": map[string]any{
			"Comment": "Redirect www.thailandgiftshop.com requests to the apex host",
			"Runtime": "cloudfront-js-2.0",
		},
		"Name": "thailandgiftshop-www-to-apex",
	})

	template.HasResourceProperties(jsii.String("AWS::CloudFront::Distribution"), map[string]any{
		"DistributionConfig": map[string]any{
			"Aliases": assertions.Match_ArrayWith(&[]any{
				siteDomainName,
				wwwDomainName,
			}),
			"CacheBehaviors": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"AllowedMethods": assertions.Match_ArrayEquals(&[]any{
						"GET",
						"HEAD",
					}),
					"CachePolicyId":           "658327ea-f89d-4fab-a63d-7e88639e58f6",
					"Compress":                true,
					"PathPattern":             "static/*",
					"FunctionAssociations":    viewerRequestFunctionAssociationAssertion(),
					"ResponseHeadersPolicyId": assertions.Match_AnyValue(),
					"ViewerProtocolPolicy":    "redirect-to-https",
				}),
			}),
			"DefaultCacheBehavior": map[string]any{
				"FunctionAssociations": viewerRequestFunctionAssociationAssertion(),
				"AllowedMethods": assertions.Match_ArrayEquals(&[]any{
					"GET",
					"HEAD",
					"OPTIONS",
					"PUT",
					"PATCH",
					"POST",
					"DELETE",
				}),
				"CachePolicyId": assertions.Match_ObjectLike(&map[string]any{
					"Ref": assertions.Match_StringLikeRegexp(jsii.String("SsrCachePolicy")),
				}),
				"Compress":                true,
				"OriginRequestPolicyId":   assertions.Match_AnyValue(),
				"ResponseHeadersPolicyId": assertions.Match_AnyValue(),
				"ViewerProtocolPolicy":    "redirect-to-https",
			},
			"Origins": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"CustomOriginConfig": map[string]any{
						"OriginProtocolPolicy": "https-only",
					},
					"DomainName": map[string]any{
						"Fn::Join": assertions.Match_ArrayWith(&[]any{
							"",
							assertions.Match_ArrayWith(&[]any{
								".execute-api.",
							}),
						}),
					},
				}),
				assertions.Match_ObjectLike(&map[string]any{
					"OriginAccessControlId": assertions.Match_AnyValue(),
					"S3OriginConfig": map[string]any{
						"OriginAccessIdentity": "",
					},
				}),
			}),
			"ViewerCertificate": assertions.Match_ObjectLike(&map[string]any{
				"SslSupportMethod": "sni-only",
			}),
		},
	})

	template.HasResourceProperties(jsii.String("AWS::CloudFront::OriginRequestPolicy"), map[string]any{
		"OriginRequestPolicyConfig": map[string]any{
			"Name": ssrOriginRequestPolicyName,
			"CookiesConfig": map[string]any{
				"CookieBehavior": "all",
			},
			"QueryStringsConfig": map[string]any{
				"QueryStringBehavior": "all",
			},
			"HeadersConfig": map[string]any{
				"HeaderBehavior": "whitelist",
				"Headers": assertions.Match_ArrayWith(&[]any{
					"CloudFront-Viewer-Address",
					"CloudFront-Forwarded-Proto",
					"Content-Type",
					"HX-Request",
					"X-CSRF-Token",
					"Stripe-Signature",
				}),
			},
		},
	})

	template.HasResourceProperties(jsii.String("AWS::CloudFront::CachePolicy"), map[string]any{
		"CachePolicyConfig": map[string]any{
			"Name":       ssrCachePolicyName,
			"MinTTL":     float64(0),
			"DefaultTTL": float64(0),
			"MaxTTL":     float64(86400),
			"ParametersInCacheKeyAndForwardedToOrigin": map[string]any{
				"CookiesConfig": map[string]any{
					"CookieBehavior": "whitelist",
					"Cookies": assertions.Match_ArrayEquals(&[]any{
						cartsession.CookieName,
						commerce.SessionCookieName,
					}),
				},
				"QueryStringsConfig": map[string]any{
					"QueryStringBehavior": "all",
				},
				"HeadersConfig": map[string]any{
					"HeaderBehavior": "none",
				},
				"EnableAcceptEncodingGzip":   true,
				"EnableAcceptEncodingBrotli": true,
			},
		},
	})

	for _, record := range []struct {
		name       string
		recordType string
	}{
		{name: siteDomainName + ".", recordType: "A"},
		{name: siteDomainName + ".", recordType: "AAAA"},
		{name: wwwDomainName + ".", recordType: "A"},
		{name: wwwDomainName + ".", recordType: "AAAA"},
	} {
		template.HasResourceProperties(jsii.String("AWS::Route53::RecordSet"), map[string]any{
			"AliasTarget": assertions.Match_ObjectLike(&map[string]any{
				"DNSName": assertions.Match_AnyValue(),
			}),
			"Name": record.name,
			"Type": record.recordType,
		})
	}

	template.HasResourceProperties(jsii.String("Custom::CDKBucketDeployment"), map[string]any{
		"DestinationBucketKeyPrefix": "static",
		"DistributionPaths": assertions.Match_ArrayWith(&[]any{
			"/static/*",
		}),
		"Prune": true,
		"SystemMetadata": map[string]any{
			"cache-control": "max-age=3600",
		},
	})

	template.HasOutput(jsii.String("SiteDistributionDomainName"), map[string]any{
		"Description": "CloudFront domain name for thailandgiftshop.com",
		"Value":       assertions.Match_AnyValue(),
	})
	template.HasOutput(jsii.String("SiteUrl"), map[string]any{
		"Description": "CloudFront URL for thailandgiftshop.com",
		"Value":       "https://" + siteDomainName,
	})
}

func TestStackIncludesProductImagesBucketAndDeployment(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.ResourceCountIs(jsii.String("AWS::S3::Bucket"), jsii.Number(2))
	template.ResourceCountIs(jsii.String("AWS::CloudFront::OriginAccessControl"), jsii.Number(2))
	template.ResourceCountIs(jsii.String("Custom::CDKBucketDeployment"), jsii.Number(2))

	template.HasResourceProperties(jsii.String("AWS::CloudFront::Distribution"), map[string]any{
		"DistributionConfig": map[string]any{
			"CacheBehaviors": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"AllowedMethods": assertions.Match_ArrayEquals(&[]any{
						"GET",
						"HEAD",
					}),
					"CachePolicyId":           "658327ea-f89d-4fab-a63d-7e88639e58f6",
					"Compress":                true,
					"PathPattern":             "images/*",
					"FunctionAssociations":    viewerRequestFunctionAssociationAssertion(),
					"ResponseHeadersPolicyId": assertions.Match_AnyValue(),
					"ViewerProtocolPolicy":    "redirect-to-https",
				}),
			}),
		},
	})

	template.HasResource(jsii.String("AWS::S3::Bucket"), map[string]any{
		"DeletionPolicy":      "Retain",
		"UpdateReplacePolicy": "Retain",
		"Properties": assertions.Match_ObjectLike(&map[string]any{
			"BucketEncryption": map[string]any{
				"ServerSideEncryptionConfiguration": assertions.Match_ArrayWith(&[]any{
					assertions.Match_ObjectLike(&map[string]any{
						"ServerSideEncryptionByDefault": map[string]any{
							"SSEAlgorithm": "AES256",
						},
					}),
				}),
			},
			"OwnershipControls": map[string]any{
				"Rules": assertions.Match_ArrayWith(&[]any{
					map[string]any{
						"ObjectOwnership": "BucketOwnerEnforced",
					},
				}),
			},
			"PublicAccessBlockConfiguration": map[string]any{
				"BlockPublicAcls":       true,
				"BlockPublicPolicy":     true,
				"IgnorePublicAcls":      true,
				"RestrictPublicBuckets": true,
			},
			"CorsConfiguration": map[string]any{
				"CorsRules": assertions.Match_ArrayWith(&[]any{
					assertions.Match_ObjectLike(&map[string]any{
						"AllowedHeaders": assertions.Match_ArrayWith(&[]any{
							"*",
						}),
						"AllowedMethods": assertions.Match_ArrayWith(&[]any{
							"POST",
						}),
						"AllowedOrigins": assertions.Match_ArrayWith(&[]any{
							"https://" + siteDomainName,
						}),
					}),
				}),
			},
		}),
	})

	template.HasResourceProperties(jsii.String("AWS::S3::BucketPolicy"), map[string]any{
		"PolicyDocument": map[string]any{
			"Statement": assertions.Match_ArrayWith(&[]any{
				assertions.Match_ObjectLike(&map[string]any{
					"Action": "s3:*",
					"Condition": map[string]any{
						"Bool": map[string]any{
							"aws:SecureTransport": "false",
						},
					},
					"Effect": "Deny",
				}),
				assertions.Match_ObjectLike(&map[string]any{
					"Action": "s3:GetObject",
					"Condition": map[string]any{
						"StringEquals": map[string]any{
							"AWS:SourceArn": assertions.Match_AnyValue(),
						},
					},
					"Effect": "Allow",
					"Principal": map[string]any{
						"Service": "cloudfront.amazonaws.com",
					},
				}),
			}),
		},
	})

	template.HasResourceProperties(jsii.String("Custom::CDKBucketDeployment"), map[string]any{
		"DestinationBucketKeyPrefix": productImagesKeyPrefix,
		"DistributionPaths": assertions.Match_ArrayWith(&[]any{
			"/images/*",
		}),
		"Prune": false,
		"SystemMetadata": map[string]any{
			"cache-control": "max-age=3600",
		},
	})

	template.HasOutput(jsii.String("ProductImagesBucketName"), map[string]any{
		"Description": "S3 bucket name for product image objects",
		"Value":       assertions.Match_AnyValue(),
	})
	template.HasOutput(jsii.String("ProductImagesBaseUrl"), map[string]any{
		"Description": "CloudFront base URL path for product images",
		"Value":       "https://" + siteDomainName + "/" + productImagesKeyPrefix + "/",
	})
}

func viewerRequestFunctionAssociationAssertion() any {
	return assertions.Match_ArrayWith(&[]any{
		assertions.Match_ObjectLike(&map[string]any{
			"EventType":   "viewer-request",
			"FunctionARN": assertions.Match_AnyValue(),
		}),
	})
}

func policyActionsForFunction(t *testing.T, templateJSON *map[string]any, functionName string) []string {
	t.Helper()

	resources := templateResources(t, templateJSON)
	roleID := lambdaRoleID(t, resources, functionName)
	var actions []string
	for _, resource := range resources {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::IAM::Policy" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		if !policyAppliesToRole(properties["Roles"], roleID) {
			continue
		}
		policyDocument := asStringMap(t, properties["PolicyDocument"])
		for _, statement := range asSlice(t, policyDocument["Statement"]) {
			statementMap := asStringMap(t, statement)
			actions = append(actions, policyStatementActions(t, statementMap["Action"])...)
		}
	}

	return actions
}

func policyForFunctionHasActionOnCatalogTable(t *testing.T, templateJSON *map[string]any, functionName string, expectedAction string) bool {
	t.Helper()

	return policyForFunctionHasActionOnResource(t, templateJSON, functionName, expectedAction, catalogTableName, "CatalogTable")
}

func policyForFunctionHasActionOnCommerceTable(t *testing.T, templateJSON *map[string]any, functionName string, expectedAction string) bool {
	t.Helper()

	return policyForFunctionHasActionOnResource(t, templateJSON, functionName, expectedAction, commerceTableName, "CommerceTable")
}

func policyForFunctionHasActionOnAdminLoginAttemptsTable(t *testing.T, templateJSON *map[string]any, functionName string, expectedAction string) bool {
	t.Helper()

	return policyForFunctionHasActionOnResource(t, templateJSON, functionName, expectedAction, "AdminLoginAttemptsTable")
}

func policyForFunctionHasActionOnResource(t *testing.T, templateJSON *map[string]any, functionName string, expectedAction string, resourceMarkers ...string) bool {
	t.Helper()

	resources := templateResources(t, templateJSON)
	roleID := lambdaRoleID(t, resources, functionName)
	for _, resource := range resources {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::IAM::Policy" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		if !policyAppliesToRole(properties["Roles"], roleID) {
			continue
		}
		policyDocument := asStringMap(t, properties["PolicyDocument"])
		for _, statement := range asSlice(t, policyDocument["Statement"]) {
			statementMap := asStringMap(t, statement)
			if containsAction(policyStatementActions(t, statementMap["Action"]), expectedAction) && statementResourceReferencesAny(t, statementMap["Resource"], resourceMarkers) {
				return true
			}
		}
	}

	return false
}

func statementResourceReferencesAny(t *testing.T, value any, markers []string) bool {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal policy resource: %v", err)
	}
	for _, marker := range markers {
		if strings.Contains(string(encoded), marker) {
			return true
		}
	}

	return false
}

func byteMatchAssertion(fieldName string, search string, positionalConstraint string) any {
	return assertions.Match_ObjectLike(&map[string]any{
		"ByteMatchStatement": map[string]any{
			"FieldToMatch": map[string]any{
				fieldName: map[string]any{},
			},
			"SearchString":         search,
			"PositionalConstraint": positionalConstraint,
			"TextTransformations": []any{
				map[string]any{"Priority": 0, "Type": "NONE"},
			},
		},
	})
}

// urlDecodedPathMatchAssertion asserts a URI-path byte match that URL-decodes
// the path before comparing, so percent-encoded spellings of rate-limited
// auth paths cannot sidestep the edge rate rules.
func urlDecodedPathMatchAssertion(search string, positionalConstraint string) any {
	return assertions.Match_ObjectLike(&map[string]any{
		"ByteMatchStatement": map[string]any{
			"FieldToMatch": map[string]any{
				"UriPath": map[string]any{},
			},
			"SearchString":         search,
			"PositionalConstraint": positionalConstraint,
			"TextTransformations": []any{
				map[string]any{"Priority": 0, "Type": "URL_DECODE"},
				map[string]any{"Priority": 1, "Type": "NONE"},
			},
		},
	})
}

func cloudFrontAPIOriginCustomHeaderValue(t *testing.T, templateJSON *map[string]any, headerName string) any {
	t.Helper()

	for _, resource := range templateResources(t, templateJSON) {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::CloudFront::Distribution" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		distributionConfig := asStringMap(t, properties["DistributionConfig"])
		for _, origin := range asSlice(t, distributionConfig["Origins"]) {
			originMap := asStringMap(t, origin)
			if !strings.Contains(templateValueString(t, originMap["DomainName"]), ".execute-api.") {
				continue
			}
			for _, header := range asSlice(t, originMap["OriginCustomHeaders"]) {
				headerMap := asStringMap(t, header)
				if headerMap["HeaderName"] == headerName {
					return headerMap["HeaderValue"]
				}
			}
			t.Fatalf("API Gateway origin missing custom header %q: %#v", headerName, originMap)
		}
	}

	t.Fatal("CloudFront API Gateway origin not found")
	return nil
}

func lambdaEnvironmentVariables(t *testing.T, templateJSON *map[string]any, functionName string) map[string]any {
	t.Helper()

	for _, resource := range templateResources(t, templateJSON) {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::Lambda::Function" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		if properties["FunctionName"] != functionName {
			continue
		}
		environment := asStringMap(t, properties["Environment"])
		return asStringMap(t, environment["Variables"])
	}

	t.Fatalf("lambda function %s not found", functionName)
	return nil
}

func templateValueString(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal template value: %v", err)
	}
	return string(encoded)
}

func templateResources(t *testing.T, templateJSON *map[string]any) map[string]any {
	t.Helper()

	resources, ok := (*templateJSON)["Resources"].(map[string]any)
	if !ok {
		t.Fatalf("template Resources has unexpected shape: %#v", (*templateJSON)["Resources"])
	}

	return resources
}

func managedLogGroupExists(t *testing.T, templateJSON *map[string]any, logGroupName string) bool {
	t.Helper()

	for _, resource := range templateResources(t, templateJSON) {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::Logs::LogGroup" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		if properties["LogGroupName"] == logGroupName {
			return true
		}
	}

	return false
}

func cloudWatchAlarmProperties(t *testing.T, templateJSON *map[string]any, alarmName string) map[string]any {
	t.Helper()

	for _, resource := range templateResources(t, templateJSON) {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::CloudWatch::Alarm" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		if properties["AlarmName"] == alarmName {
			return properties
		}
	}

	t.Fatalf("CloudWatch alarm %q not found", alarmName)
	return nil
}

func assertCriticalAlarmActions(t *testing.T, templateJSON *map[string]any, criticalAlarmNames map[string]bool) {
	t.Helper()

	criticalCount := 0
	for _, resource := range templateResources(t, templateJSON) {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::CloudWatch::Alarm" {
			continue
		}

		properties := asStringMap(t, resourceMap["Properties"])
		alarmName, ok := properties["AlarmName"].(string)
		if !ok {
			t.Fatalf("CloudWatch alarm missing string AlarmName: %#v", properties)
		}

		if criticalAlarmNames[alarmName] {
			criticalCount++
			if properties["ActionsEnabled"] != true {
				t.Fatalf("critical alarm %s must enable actions: %#v", alarmName, properties)
			}
			alarmActions, found := properties["AlarmActions"]
			if !found || !strings.Contains(templateValueString(t, alarmActions), "OperationsAlarmTopic") {
				t.Fatalf("critical alarm %s missing OperationsAlarmTopic alarm action: %#v", alarmName, properties)
			}

			continue
		}

		if properties["ActionsEnabled"] != false {
			t.Fatalf("dashboard-only alarm %s must keep actions disabled: %#v", alarmName, properties)
		}
		if _, found := properties["AlarmActions"]; found {
			t.Fatalf("dashboard-only alarm %s must not have alarm actions: %#v", alarmName, properties)
		}
	}

	if criticalCount != len(criticalAlarmNames) {
		t.Fatalf("critical alarm action count = %d, want %d", criticalCount, len(criticalAlarmNames))
	}
}

func lambdaRoleID(t *testing.T, resources map[string]any, functionName string) string {
	t.Helper()

	for _, resource := range resources {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::Lambda::Function" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		if properties["FunctionName"] != functionName {
			continue
		}
		roleReference := asStringMap(t, properties["Role"])
		getAtt := asSlice(t, roleReference["Fn::GetAtt"])
		if len(getAtt) == 0 {
			t.Fatalf("lambda %s role reference missing logical id: %#v", functionName, roleReference)
		}
		roleID, ok := getAtt[0].(string)
		if !ok {
			t.Fatalf("lambda %s role logical id has unexpected shape: %#v", functionName, getAtt[0])
		}

		return roleID
	}

	t.Fatalf("lambda function %s not found", functionName)
	return ""
}

func policyAppliesToRole(value any, roleID string) bool {
	roles, ok := value.([]any)
	if !ok {
		return false
	}
	for _, role := range roles {
		roleMap, ok := role.(map[string]any)
		if !ok {
			continue
		}
		if roleMap["Ref"] == roleID {
			return true
		}
	}

	return false
}

func policyStatementActions(t *testing.T, value any) []string {
	t.Helper()

	switch action := value.(type) {
	case string:
		return []string{action}
	case []any:
		actions := make([]string, 0, len(action))
		for _, item := range action {
			name, ok := item.(string)
			if !ok {
				t.Fatalf("policy action has unexpected shape: %#v", item)
			}
			actions = append(actions, name)
		}
		return actions
	default:
		t.Fatalf("policy action has unexpected shape: %#v", value)
		return nil
	}
}

func policyForFunctionHasSESAction(t *testing.T, templateJSON *map[string]any, functionName string, action string) bool {
	t.Helper()

	resources := templateResources(t, templateJSON)
	roleID := lambdaRoleID(t, resources, functionName)
	for _, resource := range resources {
		resourceMap := asStringMap(t, resource)
		if resourceMap["Type"] != "AWS::IAM::Policy" {
			continue
		}
		properties := asStringMap(t, resourceMap["Properties"])
		if !policyAppliesToRole(properties["Roles"], roleID) {
			continue
		}
		policyDocument := asStringMap(t, properties["PolicyDocument"])
		for _, statement := range asSlice(t, policyDocument["Statement"]) {
			statementMap := asStringMap(t, statement)
			if !containsAction(policyStatementActions(t, statementMap["Action"]), action) {
				continue
			}
			resourceText := templateValueString(t, statementMap["Resource"])
			if resourceText == `"*"` {
				t.Fatalf("%s %s resource must be scoped to the SES identity, got wildcard", functionName, action)
			}
			if strings.Contains(resourceText, ":identity/thailandgiftshop.com") || strings.Contains(resourceText, "SiteEmailIdentity") {
				return true
			}
		}
	}

	return false
}

func containsAction(actions []string, expected string) bool {
	return slices.Contains(actions, expected)
}

func asStringMap(t *testing.T, value any) map[string]any {
	t.Helper()

	valueMap, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %#v", value)
	}

	return valueMap
}

func asSlice(t *testing.T, value any) []any {
	t.Helper()

	valueSlice, ok := value.([]any)
	if !ok {
		t.Fatalf("expected []any, got %#v", value)
	}

	return valueSlice
}
