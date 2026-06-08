package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	adminauth "github.com/anhydrous99/thailandgiftshop/internal/admin"
	cartsession "github.com/anhydrous99/thailandgiftshop/internal/cart"
	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	appobservability "github.com/anhydrous99/thailandgiftshop/internal/observability"
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
		"FunctionName": "thailandgiftshop-ssr",
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
	template.ResourceCountIs(jsii.String("AWS::CloudWatch::Alarm"), jsii.Number(16))
	template.AllResourcesProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
		"ActionsEnabled":   false,
		"TreatMissingData": "notBreaching",
	})

	appAlarmMetrics := map[string]string{
		"ThailandGiftshop-Admin-OriginRejected":      appobservability.MetricAdminOriginRejected,
		"ThailandGiftshop-CatalogWrite-Errors":       appobservability.MetricCatalogWrite,
		"ThailandGiftshop-ProductImageUpload-Errors": appobservability.MetricProductImageUpload,
	}
	for alarmName, metricName := range appAlarmMetrics {
		template.HasResourceProperties(jsii.String("AWS::CloudWatch::Alarm"), map[string]any{
			"AlarmName": alarmName,
		})
		alarmText := templateValueString(t, cloudWatchAlarmProperties(t, templateJSON, alarmName))
		for _, want := range []string{appobservability.Namespace, metricName, "Service", "admin"} {
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
		"Alarm actions are intentionally disabled.",
	} {
		if !strings.Contains(templateText, want) {
			t.Fatalf("observability template missing %q", want)
		}
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
		"FunctionName": "thailandgiftshop-ssr",
		"Environment": map[string]any{
			"Variables": assertions.Match_ObjectLike(&map[string]any{
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
		"FunctionName": "thailandgiftshop-admin",
		"Environment": map[string]any{
			"Variables": map[string]any{
				"CATALOG_TABLE_NAME":                     assertions.Match_AnyValue(),
				"CATALOG_SLUG_INDEX_NAME":                catalog.DefaultSlugIndexName,
				"CATALOG_PUBLIC_INDEX_NAME":              catalog.DefaultPublicIndexName,
				"CATALOG_RECENT_INDEX_NAME":              catalog.DefaultRecentIndexName,
				adminauth.EnvProductImagesBucketName:     assertions.Match_AnyValue(),
				adminauth.EnvProductImagesKeyPrefix:      productImagesKeyPrefix,
				adminauth.EnvAdminCredentialsSecretJSON:  assertions.Match_AnyValue(),
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
	for _, action := range []string{"dynamodb:PutItem", "dynamodb:UpdateItem", "dynamodb:DeleteItem", "dynamodb:TransactWriteItems", "s3:PutObject"} {
		if containsAction(ssrActions, action) {
			t.Fatalf("public SSR lambda must not include %s; got %v", action, ssrActions)
		}
	}

	if !policyForFunctionHasActionOnCatalogTable(t, templateJSON, "thailandgiftshop-admin", "dynamodb:TransactWriteItems") {
		t.Fatalf("admin lambda policy must allow dynamodb:TransactWriteItems on the catalog table; got %v", adminActions)
	}
	if policyForFunctionHasActionOnCatalogTable(t, templateJSON, "thailandgiftshop-ssr", "dynamodb:TransactWriteItems") {
		t.Fatalf("public SSR lambda must not allow dynamodb:TransactWriteItems on the catalog table; got %v", ssrActions)
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
									byteMatchAssertion("UriPath", "/admin/login", "EXACTLY"),
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

func TestStackWiresAdminCredentialsSecretReference(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)
	template.ResourceCountIs(jsii.String("AWS::SecretsManager::Secret"), jsii.Number(2))

	templateJSON := template.ToJSON()
	adminVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-admin")
	secretReference := templateValueString(t, adminVariables[adminauth.EnvAdminCredentialsSecretJSON])
	if !strings.Contains(secretReference, adminCredentialsSecretName) || !strings.Contains(secretReference, "SecretString") {
		t.Fatalf("admin credential reference = %q, want dynamic reference to named Secrets Manager JSON secret", secretReference)
	}

	ssrVariables := lambdaEnvironmentVariables(t, templateJSON, "thailandgiftshop-ssr")
	if _, found := ssrVariables[adminauth.EnvAdminCredentialsSecretJSON]; found {
		t.Fatalf("public SSR lambda must not receive %s; got %#v", adminauth.EnvAdminCredentialsSecretJSON, ssrVariables)
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
					"ResponseHeadersPolicyId": assertions.Match_AnyValue(),
					"ViewerProtocolPolicy":    "redirect-to-https",
				}),
			}),
			"DefaultCacheBehavior": map[string]any{
				"AllowedMethods": assertions.Match_ArrayEquals(&[]any{
					"GET",
					"HEAD",
					"OPTIONS",
					"PUT",
					"PATCH",
					"POST",
					"DELETE",
				}),
				"CachePolicyId":           "4135ea2d-6df8-44a3-9df3-4b5a84be39ad",
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
				}),
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
			if containsAction(policyStatementActions(t, statementMap["Action"]), expectedAction) && statementResourceReferencesCatalogTable(t, statementMap["Resource"]) {
				return true
			}
		}
	}

	return false
}

func policyForFunctionHasActionOnAdminLoginAttemptsTable(t *testing.T, templateJSON *map[string]any, functionName string, expectedAction string) bool {
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
			if containsAction(policyStatementActions(t, statementMap["Action"]), expectedAction) && statementResourceReferencesAdminLoginAttemptsTable(t, statementMap["Resource"]) {
				return true
			}
		}
	}

	return false
}

func statementResourceReferencesCatalogTable(t *testing.T, value any) bool {
	t.Helper()

	switch resource := value.(type) {
	case string:
		return strings.Contains(resource, catalogTableName)
	case []any:
		for _, item := range resource {
			if statementResourceReferencesCatalogTable(t, item) {
				return true
			}
		}
		return false
	case map[string]any:
		encoded, err := json.Marshal(resource)
		if err != nil {
			t.Fatalf("marshal policy resource: %v", err)
		}
		return strings.Contains(string(encoded), catalogTableName) || strings.Contains(string(encoded), "CatalogTable")
	default:
		return false
	}
}

func statementResourceReferencesAdminLoginAttemptsTable(t *testing.T, value any) bool {
	t.Helper()

	switch resource := value.(type) {
	case string:
		return strings.Contains(resource, "AdminLoginAttemptsTable")
	case []any:
		for _, item := range resource {
			if statementResourceReferencesAdminLoginAttemptsTable(t, item) {
				return true
			}
		}
		return false
	case map[string]any:
		encoded, err := json.Marshal(resource)
		if err != nil {
			t.Fatalf("marshal policy resource: %v", err)
		}
		return strings.Contains(string(encoded), "AdminLoginAttemptsTable")
	default:
		return false
	}
}

func byteMatchAssertion(fieldName string, search string, positionalConstraint string) any {
	return assertions.Match_ObjectLike(&map[string]any{
		"ByteMatchStatement": map[string]any{
			"FieldToMatch": map[string]any{
				fieldName: map[string]any{},
			},
			"SearchString":         search,
			"PositionalConstraint": positionalConstraint,
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
